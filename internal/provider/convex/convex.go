// Package convex implements the pinned Management/Deployment HTTP contracts.
// It never invokes the Convex CLI and never returns email, raw provider errors,
// or credential-bearing response bodies.
package convex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"eve/internal/domain"
	"eve/internal/envfile"
)

const ManagementOrigin = "https://api.convex.dev/v1"
const maxResponse = 2 << 20

var resourceName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var originPattern = regexp.MustCompile(`^https://[a-z0-9-]+(?:\.[a-z0-9-]+)?\.convex\.(cloud|site)$`)

func failure(code, message string) error { return &domain.Error{Code: code, Message: message} }
func ValidProject(binding string) (team, project string, ok bool) {
	team, project, found := strings.Cut(binding, ":")
	return team, project, found && resourceName.MatchString(team) && resourceName.MatchString(project)
}
func SafeOrigin(origin, kind string) bool {
	return originPattern.MatchString(origin) && strings.HasSuffix(origin, ".convex."+kind)
}

type Client struct {
	token string
	http  *http.Client
}

func (c Client) String() string   { return "convex.Client" }
func (c Client) GoString() string { return c.String() }

func New(managementToken string, transport http.RoundTripper) (*Client, error) {
	if managementToken == "" || strings.ContainsAny(managementToken, "\r\n\x00") {
		return nil, failure("E_PROVIDER_AUTH", "management token is missing or invalid for transport")
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &Client{token: managementToken, http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return client, nil
}
func (c *Client) request(ctx context.Context, method, endpoint, authorization string, input, output any) (int, error) {
	return c.requestWithAbsence(ctx, method, endpoint, authorization, input, output, false)
}

func (c *Client) requestWithAbsence(ctx context.Context, method, endpoint, authorization string, input, output any, allowNotFound bool) (int, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, failure("E_PROVIDER_CONTRACT", "provider request could not be encoded")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, failure("E_PROVIDER_CONTRACT", "provider endpoint is invalid")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		code := "E_PROVIDER_TRANSPORT"
		if method != "GET" && method != "HEAD" && method != "DELETE" {
			code = "E_PROVIDER_AMBIGUOUS"
		}
		return 0, failure(code, "provider transport failed; non-idempotent write outcome was not retried")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 400:
		return response.StatusCode, failure("E_PROVIDER_VALIDATION", "provider rejected the request; requested identity/expiry was not changed")
	case 401:
		return response.StatusCode, failure("E_PROVIDER_AUTH", "provider denied the management credential")
	case 403:
		return response.StatusCode, failure("E_PROVIDER_FORBIDDEN", "provider denied this exact operation")
	case 404:
		if allowNotFound {
			return response.StatusCode, nil
		}
		return response.StatusCode, failure("E_PROVIDER_NOT_FOUND", "provider object is absent; no requested mutation was confirmed")
	case 409:
		return response.StatusCode, failure("E_PROVIDER_CONFLICT", "existing provider identity conflicts with the exact intent")
	case 429:
		return response.StatusCode, failure("E_PROVIDER_THROTTLED", "provider throttled the request")
	}
	if response.StatusCode >= 500 {
		code := "E_PROVIDER_SERVER"
		if method != "GET" && method != "HEAD" && method != "DELETE" {
			code = "E_PROVIDER_AMBIGUOUS"
		}
		return response.StatusCode, failure(code, "provider server error; write status is retained for reconciliation")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, failure("E_PROVIDER_CONTRACT", "unexpected provider status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		if method != "GET" && method != "HEAD" && method != "DELETE" {
			return response.StatusCode, failure("E_PROVIDER_AMBIGUOUS", "provider write response was unreadable; outcome was not retried")
		}
		return response.StatusCode, failure("E_PROVIDER_CONTRACT", "provider response was unreadable or exceeded 2 MiB")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		if method != "GET" && method != "HEAD" && method != "DELETE" {
			return response.StatusCode, failure("E_PROVIDER_AMBIGUOUS", "provider write response was uncertain; outcome was not retried")
		}
		return response.StatusCode, failure("E_PROVIDER_CONTRACT", "provider response does not match the pinned wire contract")
	}
	return response.StatusCode, nil
}
func (c *Client) management(ctx context.Context, method, path string, input, output any) (int, error) {
	return c.request(ctx, method, ManagementOrigin+path, "Bearer "+c.token, input, output)
}

func (c *Client) managementWithAbsence(ctx context.Context, method, path string, input, output any) (int, error) {
	return c.requestWithAbsence(ctx, method, ManagementOrigin+path, "Bearer "+c.token, input, output, true)
}
func (c *Client) deployed(ctx context.Context, d Deployment, key, method, path string, input, output any) (int, error) {
	if !SafeOrigin(d.URL, "cloud") {
		return 0, failure("E_PROVIDER_CONTRACT", "deployment credential origin is outside the supported Convex set")
	}
	return c.request(ctx, method, d.URL+"/api/v1/"+path, "Convex "+key, input, output)
}

type Project struct {
	ID       int64  `json:"id"`
	TeamID   int64  `json:"teamId"`
	Slug     string `json:"slug"`
	TeamSlug string `json:"teamSlug"`
	Dev      string `json:"devDeploymentName"`
	Prod     string `json:"prodDeploymentName"`
}
type Deployment struct {
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
type Intent struct {
	ProjectID          int64
	Reference          string
	Region             string
	StartMS, ExpiresMS int64
}
type CreateDeploymentArgs struct {
	Type      string `json:"type"`
	IsDefault bool   `json:"isDefault"`
	Reference string `json:"reference"`
	ExpiresAt int64  `json:"expiresAt"`
	Region    string `json:"region,omitempty"`
}
type Key struct{ name, value string }

func (k Key) String() string   { return "convex.deploy-key" }
func (k Key) GoString() string { return k.String() }
func (k Key) Name() string     { return k.name }
func (k Key) Value() string    { return k.value }

func (c *Client) ValidateProject(ctx context.Context, binding string) (Project, error) {
	team, slug, ok := ValidProject(binding)
	if !ok {
		return Project{}, failure("E_PROVIDER_IDENTITY", "explicit lower-case team:project binding is required")
	}
	var project Project
	_, err := c.management(ctx, "GET", "/teams/"+team+"/projects/"+slug, nil, &project)
	if err != nil {
		return Project{}, err
	}
	var token struct {
		Type   string `json:"type"`
		TeamID int64  `json:"teamId"`
	}
	if _, err := c.management(ctx, "GET", "/token_details", nil, &token); err != nil {
		return Project{}, err
	}
	if project.ID <= 0 || project.TeamID <= 0 || project.TeamSlug != team || project.Slug != slug || token.Type != "teamToken" || token.TeamID != project.TeamID || project.Dev == "" || !resourceName.MatchString(project.Dev) || (project.Prod != "" && !resourceName.MatchString(project.Prod)) {
		return Project{}, failure("E_PROVIDER_IDENTITY", "team token/project identity or existing default dev deployment did not match")
	}
	return project, nil
}
func (c *Client) InspectDefault(ctx context.Context, name, kind string, projectID int64) (Deployment, error) {
	var d Deployment
	if _, err := c.management(ctx, "GET", "/deployments/"+name, nil, &d); err != nil {
		return Deployment{}, err
	}
	if d.ID <= 0 || d.Name != name || d.ProjectID != projectID || d.Kind != "cloud" || d.Type != kind || d.Default == nil || !*d.Default {
		return Deployment{}, failure("E_PROVIDER_IDENTITY", "default deployment identity differs from the recorded project")
	}
	return d, nil
}
func VerifyDeployment(d Deployment, in Intent) error {
	if d.ID <= 0 || !resourceName.MatchString(d.Name) || d.ProjectID != in.ProjectID || d.Kind != "cloud" || d.Type != "dev" || d.Default == nil || *d.Default || d.Reference != in.Reference || !SafeOrigin(d.URL, "cloud") || d.ExpiresAt != in.ExpiresMS || d.CreatedAt < in.StartMS-60_000 || d.CreatedAt > in.StartMS+120_000 {
		return failure("E_PROVIDER_CONTRACT", "deployment identity/type/default/reference/URL/expiry/creation window mismatch; automatic deletion refused")
	}
	return nil
}
func (c *Client) Lookup(ctx context.Context, in Intent) (*Deployment, bool, error) {
	var d Deployment
	status, err := c.managementWithAbsence(ctx, "GET", fmt.Sprintf("/projects/%d/deployment?reference=%s", in.ProjectID, url.QueryEscape(in.Reference)), nil, &d)
	if status == 404 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := VerifyDeployment(d, in); err != nil {
		return nil, false, err
	}
	return &d, true, nil
}
func (c *Client) Inspect(ctx context.Context, in Intent, observed Deployment) (Deployment, error) {
	var d Deployment
	status, err := c.managementWithAbsence(ctx, "GET", "/deployments/"+observed.Name, nil, &d)
	if status == 404 {
		return Deployment{}, failure("E_PROVIDER_NOT_FOUND", "recorded deployment no longer exists")
	}
	if err != nil {
		return Deployment{}, err
	}
	if err := VerifyDeployment(d, in); err != nil {
		return Deployment{}, err
	}
	if d.ID != observed.ID || d.Name != observed.Name {
		return Deployment{}, failure("E_PROVIDER_IDENTITY", "recorded deployment identity changed; deletion refused")
	}
	return d, nil
}
func (c *Client) Create(ctx context.Context, in Intent) (*Deployment, error) {
	if in.ProjectID <= 0 || in.Reference == "" || in.ExpiresMS <= 0 || in.StartMS <= 0 {
		return nil, failure("E_PROVIDER_INTENT", "invalid resource creation intent")
	}
	if _, found, err := c.Lookup(ctx, in); err != nil || found {
		if err != nil {
			return nil, err
		}
		return nil, failure("E_PROVIDER_CONFLICT", "an owned-looking resource reference already exists; do not adopt it")
	}
	request := CreateDeploymentArgs{Type: "dev", IsDefault: false, Reference: in.Reference, ExpiresAt: in.ExpiresMS, Region: in.Region}
	var d Deployment
	if _, err := c.management(ctx, "POST", fmt.Sprintf("/projects/%d/create_deployment", in.ProjectID), request, &d); err != nil {
		return nil, err
	}
	if err := VerifyDeployment(d, in); err != nil {
		return nil, err
	}
	return &d, nil
}
func (c *Client) CreateDeployKey(ctx context.Context, d Deployment, name string, expires int64) (*Key, error) {
	if name == "" || len(name) > 128 || expires <= 0 || strings.ContainsAny(name, "\r\n\x00|") {
		return nil, failure("E_PROVIDER_INTENT", "deploy key name/expiry is outside the intended contract")
	}
	var out struct {
		Value string `json:"deployKey"`
	}
	if _, err := c.management(ctx, "POST", "/deployments/"+d.Name+"/create_deploy_key", struct {
		Name      string `json:"name"`
		ExpiresAt int64  `json:"expiresAt"`
	}{name, expires}, &out); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(out.Value, "dev:"+d.Name+"|") || strings.HasSuffix(out.Value, "|") || envfile.ValidateValue(out.Value) != nil {
		return nil, failure("E_PROVIDER_CONTRACT", "native deployment key format is unsupported by this adapter")
	}
	return &Key{name: name, value: out.Value}, nil
}

type DeployKeyMetadata struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expiresAt"`
}

func (c *Client) VerifyDeployKeyMetadata(ctx context.Context, d Deployment, key Key, expires int64) (Key, error) {
	var keys []DeployKeyMetadata
	if _, err := c.management(ctx, "GET", "/deployments/"+d.Name+"/list_deploy_keys", nil, &keys); err != nil {
		return key, err
	}
	found := 0
	for _, entry := range keys {
		if entry.Name == key.name {
			found++
			if entry.ID <= 0 || entry.ExpiresAt != expires {
				return key, failure("E_PROVIDER_CONTRACT", "deploy key identity/expiry metadata mismatch")
			}
		}
	}
	if found != 1 {
		return key, failure("E_PROVIDER_IDENTITY", "owned deploy key name is not uniquely visible")
	}
	return key, nil
}

func (c *Client) DeployKeyMetadata(ctx context.Context, d Deployment) ([]DeployKeyMetadata, error) {
	var keys []DeployKeyMetadata
	_, err := c.management(ctx, "GET", "/deployments/"+d.Name+"/list_deploy_keys", nil, &keys)
	return keys, err
}

func (c *Client) DeleteDeployKey(ctx context.Context, d Deployment, name string) error {
	keys, err := c.DeployKeyMetadata(ctx, d)
	if err != nil {
		return err
	}
	matched := 0
	var id int64
	for _, key := range keys {
		if key.Name == name {
			matched++
			id = key.ID
		}
	}
	if matched == 0 {
		return failure("E_PROVIDER_NOT_FOUND", "recorded deploy key is already absent")
	}
	if matched != 1 || id <= 0 {
		return failure("E_PROVIDER_IDENTITY", "deploy key name does not resolve to one owned identity")
	}
	_, err = c.management(ctx, "POST", "/deployments/"+d.Name+"/delete_deploy_key", struct {
		ID int64 `json:"id"`
	}{id}, nil)
	return err
}
func (c *Client) CanonicalURLs(ctx context.Context, d Deployment, key string) (cloud, site string, err error) {
	var out struct {
		Cloud string `json:"convexCloudUrl"`
		Site  string `json:"convexSiteUrl"`
	}
	if _, err = c.deployed(ctx, d, key, "GET", "get_canonical_urls", nil, &out); err != nil {
		return
	}
	if out.Cloud != d.URL || !SafeOrigin(out.Site, "site") {
		err = failure("E_PROVIDER_CONTRACT", "canonical deployment/environment URLs do not match verified origins")
	}
	return out.Cloud, out.Site, err
}
func (c *Client) Env(ctx context.Context, d Deployment, key string) (map[string]string, error) {
	var out struct {
		Values map[string]string `json:"environmentVariables"`
	}
	_, err := c.deployed(ctx, d, key, "GET", "list_environment_variables", nil, &out)
	if out.Values == nil && err == nil {
		err = failure("E_PROVIDER_CONTRACT", "environment map missing")
	}
	return out.Values, err
}
func (c *Client) UpdateEnv(ctx context.Context, d Deployment, key string, changes map[string]string) error {
	// Secret/provider keys are deliberately not read from the changed set here. The
	// manifest/resolver decides ownership; unrelated values are never uploaded.
	type change struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	ordered := make([]change, 0, len(changes))
	for _, name := range sortedKeys(changes) {
		ordered = append(ordered, change{name, changes[name]})
	}
	_, err := c.deployed(ctx, d, key, "POST", "update_environment_variables", struct {
		Changes []change `json:"changes"`
	}{ordered}, nil)
	return err
}
func sortedKeys(values map[string]string) []string { return slices.Sorted(maps.Keys(values)) }
func (c *Client) Delete(ctx context.Context, in Intent, observed Deployment) error {
	exact, err := c.Inspect(ctx, in, observed)
	if err != nil {
		return err
	}
	if _, err := c.management(ctx, "POST", "/deployments/"+exact.Name+"/delete", struct{}{}, nil); err != nil {
		return err
	}
	// Auth/server errors are not absence. 404 after a successful named delete is
	// reconciled by the caller, not converted into project/resource changes.
	for range 30 {
		_, err := c.Inspect(ctx, in, exact)
		if err == nil {
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
			continue
		}
		var d *domain.Error
		if !errors.As(err, &d) || d.Code != "E_PROVIDER_NOT_FOUND" {
			return err
		}
		return nil
	}
	return failure("E_PROVIDER_AMBIGUOUS", "deletion request was accepted but disappearance is not confirmed")
}
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
