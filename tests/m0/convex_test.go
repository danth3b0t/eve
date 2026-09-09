//go:build linux || darwin

package m0

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const managementOrigin = "https://api.convex.dev/v1"
const defaultMarker = "eve-m0-public-default"

var providerName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var convexOrigin = regexp.MustCompile(`^https://[a-z0-9-]+(?:\.[a-z0-9-]+)?\.convex\.(cloud|site)$`)

type cloudAPI struct {
	token  string
	client *http.Client
}

func newCloudAPI(token string) cloudAPI {
	return cloudAPI{token: token, client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // refuse even same-origin redirects
		},
	}}
}

// Wire types are hand-reviewed against the pinned snapshots. They deliberately
// omit unrelated fields; no raw key/env response is ever journaled or printed.
type project struct {
	ID       int64  `json:"id"`
	TeamID   int64  `json:"teamId"`
	Slug     string `json:"slug"`
	TeamSlug string `json:"teamSlug"`
	Dev      string `json:"devDeploymentName"`
	Prod     string `json:"prodDeploymentName"`
}

type deployment struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ProjectID int64  `json:"projectId"`
	Kind      string `json:"kind"`
	Type      string `json:"deploymentType"`
	Default   *bool  `json:"isDefault"`
	Reference string `json:"reference"`
	URL       string `json:"deploymentUrl"`
	CreatedAt int64  `json:"createTime"`
	ExpiresAt int64  `json:"expiresAt"`
}

type creation struct {
	ProjectID int64  `json:"project_id"`
	Reference string `json:"reference"`
	KeyName   string `json:"key_name"`
	Start     int64  `json:"start_ms"`
	ExpiresAt int64  `json:"expires_at_ms"`
}

type createRequest struct {
	Type      string `json:"type"`
	IsDefault bool   `json:"isDefault"`
	Reference string `json:"reference"`
	ExpiresAt int64  `json:"expiresAt"`
	Region    string `json:"region,omitempty"`
}

func (a cloudAPI) request(ctx context.Context, method, endpoint, authorization string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, errors.New("request serialization failed")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, errors.New("request construction failed")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("transport failure; write outcome may be unknown (not retried)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("provider HTTP %d (body withheld; writes not retried)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return resp.StatusCode, errors.New("provider response unreadable or over 2 MiB; write outcome may be unknown")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return resp.StatusCode, errors.New("provider response schema mismatch (body withheld)")
	}
	return resp.StatusCode, nil
}

func (a cloudAPI) management(method, path string, input, output any) (int, error) {
	return a.request(context.Background(), method, managementOrigin+path, "Bearer "+a.token, input, output)
}

func (a cloudAPI) deployed(d deployment, key, method, path string, input, output any) (int, error) {
	if !safeOrigin(d.URL, "cloud") {
		return 0, errors.New("unapproved deployment credential origin")
	}
	return a.request(context.Background(), method, d.URL+"/api/v1/"+path, "Convex "+key, input, output)
}

func safeOrigin(origin, kind string) bool {
	return convexOrigin.MatchString(origin) && strings.HasSuffix(origin, ".convex."+kind)
}

func verifyDeployment(d deployment, intent creation) error {
	if d.ID <= 0 || !providerName.MatchString(d.Name) || d.ProjectID != intent.ProjectID ||
		d.Kind != "cloud" || d.Type != "dev" || d.Default == nil || *d.Default ||
		d.Reference != intent.Reference || !safeOrigin(d.URL, "cloud") ||
		d.ExpiresAt != intent.ExpiresAt || d.CreatedAt < intent.Start-60_000 || d.CreatedAt > intent.Start+120_000 {
		return errors.New("deployment identity/type/default/reference/URL/expiry/creation-window mismatch; automatic deletion refused")
	}
	return nil
}

func (a cloudAPI) lookup(intent creation) (deployment, int, error) {
	var d deployment
	status, err := a.management("GET", fmt.Sprintf("/projects/%d/deployment?reference=%s", intent.ProjectID, url.QueryEscape(intent.Reference)), nil, &d)
	return d, status, err
}

// This is a test-owned evidence file, NOT EVE's operation journal or a resume
// implementation. Keep it outside t.TempDir so a failed cleanup retains intent.
type cloudProbe struct {
	api      cloudAPI
	intent   creation
	observed *deployment
	key      string
	siteURL  string
	journal  *os.File
	cleaned  bool
}

func (p *cloudProbe) record(phase string) error {
	if err := json.NewEncoder(p.journal).Encode(struct {
		Phase    string      `json:"phase"`
		Intent   creation    `json:"intent"`
		Observed *deployment `json:"observed,omitempty"`
	}{phase, p.intent, p.observed}); err != nil {
		return err
	}
	return p.journal.Sync()
}

func provision(t *testing.T, api cloudAPI, projectID int64, evidence string) *cloudProbe {
	t.Helper()
	id := make([]byte, 16)
	_, err := rand.Read(id)
	must(t, err)
	id[6], id[8] = (id[6]&0x0f)|0x40, (id[8]&0x3f)|0x80
	uuid := hex.EncodeToString(id)
	now := time.Now()
	intent := creation{
		ProjectID: projectID, Reference: "dev/eve/" + uuid + "/backend-10e08a41",
		KeyName: "eve-m0-" + uuid + "-backend-g1", Start: now.UnixMilli(),
		ExpiresAt: now.Add(5 * 24 * time.Hour).UnixMilli(),
	}
	_, status, err := api.lookup(intent)
	if status != http.StatusNotFound {
		t.Fatalf("preexisting reference or failed absence check: HTTP %d, %v", status, err)
	}
	journal, err := os.OpenFile(filepath.Join(evidence, uuid+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(t, err)
	p := &cloudProbe{api: api, intent: intent, journal: journal}
	t.Cleanup(func() {
		if err := p.cleanup(); err != nil {
			t.Errorf("cloud cleanup pending: %v; retain evidence at %s", err, evidence)
		} else {
			err := os.Remove(filepath.Join(evidence, uuid+".key"))
			if err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
		}
		p.journal.Close()
	})
	must(t, p.record("create_intent"))
	var d deployment
	_, err = api.management("POST", fmt.Sprintf("/projects/%d/create_deployment", projectID), createRequest{
		Type: "dev", IsDefault: false, Reference: intent.Reference, ExpiresAt: intent.ExpiresAt,
		Region: os.Getenv("EVE_M0_REGION"),
	}, &d)
	// Even a mismatching response remains inspectable; it never authorizes delete.
	if d.ID != 0 {
		p.observed = &d
	}
	must(t, p.record("create_response_or_unknown"))
	must(t, err) // no second create; finalizer reconciles only the exact intent
	must(t, verifyDeployment(d, intent))
	p.observed = &d
	must(t, p.record("deployment_verified"))
	must(t, p.record("key_create_intent"))
	var keyResponse struct {
		Key string `json:"deployKey"`
	}
	_, err = api.management("POST", "/deployments/"+d.Name+"/create_deploy_key", struct {
		Name      string `json:"name"`
		ExpiresAt int64  `json:"expiresAt"`
	}{intent.KeyName, intent.ExpiresAt}, &keyResponse)
	must(t, err) // a lost key is cleaned up with this exact disposable deployment
	if !strings.HasPrefix(keyResponse.Key, "dev:"+d.Name+"|") || !portableValue(keyResponse.Key) || strings.HasSuffix(keyResponse.Key, "|") {
		t.Fatal("unsupported native deployment key format (value withheld)")
	}
	p.key = keyResponse.Key
	secret, err := os.OpenFile(filepath.Join(evidence, uuid+".key"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(t, err)
	_, err = secret.WriteString(p.key)
	if err == nil {
		err = secret.Sync()
	}
	closeErr := secret.Close()
	must(t, err)
	must(t, closeErr)
	var keys []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	_, err = api.management("GET", "/deployments/"+d.Name+"/list_deploy_keys", nil, &keys)
	must(t, err)
	found := 0
	for _, k := range keys {
		if k.Name == intent.KeyName {
			found++
			if k.ID <= 0 || k.ExpiresAt != intent.ExpiresAt {
				t.Fatal("key identity/expiry metadata mismatch")
			}
		}
	}
	if found != 1 {
		t.Fatal("owned key name not uniquely visible in metadata")
	}
	var canonical struct {
		Cloud string `json:"convexCloudUrl"`
		Site  string `json:"convexSiteUrl"`
	}
	_, err = api.deployed(d, p.key, "GET", "get_canonical_urls", nil, &canonical)
	must(t, err)
	if canonical.Cloud != d.URL || !safeOrigin(canonical.Site, "site") {
		t.Fatal("canonical URLs do not match the supported default-origin contract")
	}
	p.siteURL = canonical.Site
	var env struct {
		Values map[string]string `json:"environmentVariables"`
	}
	_, err = api.deployed(d, p.key, "GET", "list_environment_variables", nil, &env)
	must(t, err)
	if t.Run("development defaults", func(t *testing.T) {
		if env.Values["EVE_M0_DEFAULT"] != defaultMarker {
			t.Fatal("dev default marker missing; set EVE_M0_DEFAULT=eve-m0-public-default in project development defaults (not a local env file)")
		}
	}) {
		must(t, p.record("defaults_verified"))
	}
	must(t, p.record("key_expiry_urls_verified"))
	return p
}

func portableValue(value string) bool {
	for _, r := range value {
		if r < 33 || r > 126 || strings.ContainsRune("\"'`\\$#", r) {
			return false
		}
	}
	return true
}

func (p *cloudProbe) cleanup() error {
	if p.cleaned {
		return nil
	}
	d, status, err := p.api.lookup(p.intent)
	if status == http.StatusNotFound {
		// A single absent reference cannot settle an ambiguous creation, or
		// prove deletion of a known deployment whose reference was changed.
		if p.observed == nil {
			return errors.New("create outcome unconfirmed; no automatic retry or assumed cleanup")
		}
		if err := verifyDeployment(*p.observed, p.intent); err != nil {
			return err
		}
		var exact deployment
		byNameStatus, byNameErr := p.api.management("GET", "/deployments/"+p.observed.Name, nil, &exact)
		if byNameStatus != http.StatusNotFound {
			if byNameErr != nil {
				return byNameErr
			}
			return errors.New("owned reference missing but deployment still exists; cleanup requires review")
		}
		p.cleaned = true
		return p.record("already_absent")
	}
	if err != nil {
		return err
	}
	if err := verifyDeployment(d, p.intent); err != nil {
		return err
	}
	if p.observed != nil && (d.ID != p.observed.ID || d.Name != p.observed.Name ||
		verifyDeployment(*p.observed, p.intent) != nil) {
		return errors.New("observed deployment identity changed; deletion refused")
	}
	// Re-read by immutable recorded name as well as reference before deletion.
	var exact deployment
	_, err = p.api.management("GET", "/deployments/"+d.Name, nil, &exact)
	if err != nil {
		return err
	}
	if err := verifyDeployment(exact, p.intent); err != nil {
		return err
	}
	if exact.ID != d.ID || exact.Name != d.Name {
		return errors.New("exact lookup identity mismatch")
	}
	p.observed = &exact
	if err := p.record("delete_intent"); err != nil {
		return err
	}
	_, err = p.api.management("POST", "/deployments/"+d.Name+"/delete", struct{}{}, nil)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err = p.api.management("GET", "/deployments/"+d.Name, nil, &exact)
		if status == http.StatusNotFound {
			p.cleaned = true
			p.key = ""
			return p.record("deleted")
		}
		if err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
	return errors.New("deletion not yet confirmed")
}

func (p *cloudProbe) bind(t *testing.T, f fixture) nativeValues {
	t.Helper()
	v := nativeValues{ports: freePorts(t), url: p.observed.URL, siteURL: p.siteURL}
	site := fmt.Sprintf("http://localhost:%d", v.ports[0])
	must(t, p.record("owned_env_write_intent"))
	_, err := p.api.deployed(*p.observed, p.key, "POST", "update_environment_variables", struct {
		Changes []map[string]string `json:"changes"`
	}{[]map[string]string{{"name": "SITE_URL", "value": site}}}, nil)
	must(t, err)
	f.configureFrontends(t, v)
	f.write(t, "packages/backend/.env.local", "CONVEX_DEPLOYMENT=dev:"+p.observed.Name+"\nCONVEX_DEPLOY_KEY="+p.key+"\n")
	must(t, p.record("native_files_prepared"))
	return v
}

func assertBackend(t *testing.T, p *cloudProbe, process *running, v nativeValues) {
	t.Helper()
	waitFor(t, process, func() bool {
		var result struct {
			Status string `json:"status"`
			Value  struct {
				Site string `json:"siteUrl"`
			} `json:"value"`
		}
		_, err := p.api.request(context.Background(), "POST", v.url+"/api/query", "", map[string]any{
			"path": "probe:connection", "args": map[string]any{}, "format": "json",
		}, &result)
		return err == nil && result.Status == "success" &&
			result.Value.Site == fmt.Sprintf("http://localhost:%d", v.ports[0])
	})
}

func TestLiveConvexAndNativeLaunch(t *testing.T) {
	if os.Getenv("EVE_M0_LIVE") != "1" {
		t.Skip("set EVE_M0_LIVE=1 with EVE_M0_PROJECT and EVE_CONVEX_TOKEN; creates THREE disposable deployments")
	}
	team, slug, ok := strings.Cut(os.Getenv("EVE_M0_PROJECT"), ":")
	if !ok || !providerName.MatchString(team) || !providerName.MatchString(slug) || os.Getenv("EVE_CONVEX_TOKEN") == "" {
		t.Fatal("explicit dedicated nonproduction team:project and team token required")
	}
	api := newCloudAPI(os.Getenv("EVE_CONVEX_TOKEN"))
	var before project
	projectPath := "/teams/" + team + "/projects/" + slug
	_, err := api.management("GET", projectPath, nil, &before)
	must(t, err)
	var token struct {
		Type   string `json:"type"`
		TeamID int64  `json:"teamId"`
	}
	_, err = api.management("GET", "/token_details", nil, &token)
	must(t, err)
	if before.ID <= 0 || before.TeamSlug != team || before.Slug != slug || token.Type != "teamToken" || token.TeamID != before.TeamID {
		t.Fatal("team token/project identity mismatch")
	}
	if before.Dev == "" {
		t.Fatal("dedicated project must already have a default cloud dev deployment to test preservation")
	}
	var defaults []deployment
	for kind, name := range map[string]string{"dev": before.Dev, "prod": before.Prod} {
		if name == "" {
			continue
		}
		if !providerName.MatchString(name) {
			t.Fatal("unsupported default deployment name")
		}
		var d deployment
		_, err := api.management("GET", "/deployments/"+name, nil, &d)
		must(t, err)
		if d.ID <= 0 || d.Name != name || d.ProjectID != before.ID || d.Kind != "cloud" || d.Type != kind || d.Default == nil || !*d.Default {
			t.Fatal("default cloud deployment identity could not be established")
		}
		defaults = append(defaults, d)
	}
	f := newFixture(t) // fail local tool/install preflight before any cloud write
	evidence, err := os.MkdirTemp("", "eve-m0-cloud-")
	must(t, err)
	t.Logf("protected cloud evidence: %s", evidence)
	t.Cleanup(func() {
		var after project
		_, err := api.management("GET", projectPath, nil, &after)
		if err != nil || after != before {
			t.Errorf("project/default identities changed or could not be reverified; evidence: %s", evidence)
			return
		}
		for _, expected := range defaults {
			var d deployment
			_, err := api.management("GET", "/deployments/"+expected.Name, nil, &d)
			if err != nil || d.ID != expected.ID || d.Name != expected.Name || d.ProjectID != expected.ProjectID || d.Type != expected.Type || d.Default == nil || !*d.Default {
				t.Errorf("default deployment no longer has its original identity; evidence: %s", evidence)
			}
		}
		if !t.Failed() {
			must(t, os.RemoveAll(evidence))
		}
	})
	baseline := provision(t, api, before.ID, evidence)
	v0 := baseline.bind(t, f)
	p0 := f.start(t, "run", "dev") // exact principal launch, no filter or wrapper
	assertFrontends(t, p0, v0)
	assertBackend(t, baseline, p0, v0)
	p0.stop()
	f.unchanged(t)

	a, b := f.worktree(t, "workspace-a"), f.worktree(t, "workspace-b")
	ra := provision(t, api, before.ID, evidence)
	va := ra.bind(t, a)
	pa := a.start(t, "run", "dev")
	assertFrontends(t, pa, va)
	assertBackend(t, ra, pa, va)
	rb := provision(t, api, before.ID, evidence)
	vb := rb.bind(t, b)
	pb := b.start(t, "run", "dev")
	assertFrontends(t, pb, vb)
	assertBackend(t, rb, pb, vb)
	if ra.observed.ID == rb.observed.ID || va.url == vb.url || ra.observed.ID == baseline.observed.ID {
		t.Fatal("workspaces did not receive independent deployments")
	}
	// A's key must not authorize reading B's environment, even in the same team.
	status, _ := api.deployed(*rb.observed, ra.key, "GET", "list_environment_variables", nil, nil)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatal("deployment key isolation not proven: expected 401/403 on the other test-owned deployment")
	}
	a.unchanged(t)
	b.unchanged(t)
	pa.stop()
	must(t, ra.cleanup())
	f.run(t, "git", "-c", "core.hooksPath=/dev/null", "worktree", "remove", a.root)
	assertFrontends(t, pb, vb)
	assertBackend(t, rb, pb, vb)
	pb.stop()
	f.unchanged(t)
	// Registered cleanup finalizers run in reverse order, after launchers stop.
}
