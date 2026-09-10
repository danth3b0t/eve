package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/provider/convex"
)

type convexTransport func(*http.Request) (*http.Response, error)

func (f convexTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func body(status int, text string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}
}

func resourceFixture(t *testing.T) (repository, GitPlan) {
	t.Helper()
	r := repositoryFixture(t)
	manifest := "version = 1\n[workspace]\nport_block_size = 4\n[resources.backend]\nprovider = \"convex\"\npath = \"packages/backend\"\nproject = \"dev-team:m0\"\n[resources.backend.env]\nSITE_URL = \"${services.web.url}\"\n[services.web]\npath = \"apps/web\"\nenv_file = \".env.local\"\nport = \"PORT\"\n[services.web.env]\nVITE_CONVEX_URL = \"${resources.backend.url}\"\nVITE_CONVEX_SITE_URL = \"${resources.backend.site_url}\"\n"
	for _, dir := range []string{"packages/backend", "apps/web"} {
		if err := os.MkdirAll(filepath.Join(r.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, dir, ".keep"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{".gitignore": "**/.env.local\n", "eve.toml": manifest, "packages/backend/.env.local": "EXISTING_UNMANAGED=kept\n"} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "resource fixture")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "cloud-fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	return r, plan
}

func TestGCDeletesExactRemoteResourceAfterManualWorktreeLoss(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	identity, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	factory := fakeConvexFactory(t, make(chan map[string]string, 1), false, nil)
	bindings, err := provisionResources(t.Context(), r.store, lock, current, factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageFilesWithBindings(t.Context(), r.store, r.client, lock, plan.Files, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "worktree", "remove", "--force", "--", identity.Path)
	command(t, r.root, "worktree", "prune")
	result, err := ApplyGC(t.Context(), r.store, r.client, plan.WorkspaceID, GCOptions{ProviderFactory: factory})
	if err != nil || result.Workspace.State != "destroyed" {
		t.Fatalf("GC remote cleanup: %v", err)
	}
	resources, err := r.store.Resources(t.Context(), plan.WorkspaceID)
	if err != nil || resources[0].State != "deleted" {
		t.Fatal("remote resource not deleted")
	}
	if _, err := os.Lstat(filepath.Join(r.base, "state", "secrets", resources[0].CredentialID)); !os.IsNotExist(err) {
		t.Fatal("deploy credential retained")
	}
}

func fakeConvexFactory(t *testing.T, managed chan map[string]string, failOnce bool, attempts *int) convexFactory {
	t.Helper()
	var keyName string
	var expiresAt int64
	var posts int
	var created convex.CreateDeploymentArgs
	var deleted bool
	return func(token string) (convexAdapter, error) {
		if token != "auth-holder-sentinel" {
			return nil, fmt.Errorf("unexpected token scope")
		}
		api, err := convex.New(token, convexTransport(func(r *http.Request) (*http.Response, error) {
			data := []byte(nil)
			if r.Body != nil {
				data, _ = io.ReadAll(r.Body)
			}
			if r.URL.Host == "api.convex.dev" {
				if r.Header.Get("Authorization") != "Bearer auth-holder-sentinel" {
					return body(401, "no"), nil
				}
			} else if r.Header.Get("Authorization") != "Convex dev:calm-cow-456|convex-key" {
				return body(401, "no"), nil
			}
			route := r.Method + " " + r.URL.Host + r.URL.Path
			switch route {
			case "GET api.convex.dev/v1/teams/dev-team/projects/m0":
				return body(200, `{"id":42,"teamId":7,"slug":"m0","teamSlug":"dev-team","devDeploymentName":"default-dev-123","prodDeploymentName":""}`), nil
			case "GET api.convex.dev/v1/token_details":
				return body(200, `{"type":"teamToken","teamId":7}`), nil
			case "GET api.convex.dev/v1/deployments/default-dev-123":
				return body(200, `{"id":10,"name":"default-dev-123","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":true,"reference":"dev/original","deploymentUrl":"https://default-dev-123.convex.cloud","createTime":1,"expiresAt":0}`), nil
			case "GET api.convex.dev/v1/projects/42/deployment":
				if posts == 0 {
					return body(404, ""), nil
				}
				return body(200, fmt.Sprintf(`{"id":123,"name":"calm-cow-456","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":"https://calm-cow-456.eu-west-1.convex.cloud","createTime":%d,"expiresAt":%d}`, created.Reference, created.ExpiresAt-5*24*60*60*1000, created.ExpiresAt)), nil
			case "POST api.convex.dev/v1/projects/42/create_deployment":
				posts++
				if attempts != nil {
					*attempts = posts
				}
				var create convex.CreateDeploymentArgs
				if json.Unmarshal(data, &create) != nil || create.Type != "dev" || create.IsDefault || !strings.HasPrefix(create.Reference, "dev/eve/") {
					t.Fatal("bad creation contract")
				}
				created = create
				expiresAt = create.ExpiresAt
				if failOnce && posts == 1 {
					return body(503, ""), nil
				}
				return body(200, fmt.Sprintf(`{"id":123,"name":"calm-cow-456","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":"https://calm-cow-456.eu-west-1.convex.cloud","createTime":%d,"expiresAt":%d}`, create.Reference, expiresAt-5*24*60*60*1000, expiresAt)), nil
			case "POST api.convex.dev/v1/deployments/calm-cow-456/create_deploy_key":
				var request struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(data, &request) != nil || request.Name == "" {
					t.Fatal("bad key request")
				}
				keyName = request.Name
				return body(200, `{"deployKey":"dev:calm-cow-456|convex-key"}`), nil
			case "GET api.convex.dev/v1/deployments/calm-cow-456/list_deploy_keys":
				return body(200, fmt.Sprintf(`[{"id":9,"name":%q,"expiresAt":%d}]`, keyName, expiresAt)), nil
			case "GET api.convex.dev/v1/deployments/calm-cow-456":
				if deleted {
					return body(404, ""), nil
				}
				return body(200, fmt.Sprintf(`{"id":123,"name":"calm-cow-456","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":"https://calm-cow-456.eu-west-1.convex.cloud","createTime":%d,"expiresAt":%d}`, created.Reference, created.ExpiresAt-5*24*60*60*1000, created.ExpiresAt)), nil
			case "POST api.convex.dev/v1/deployments/calm-cow-456/delete":
				if deleted {
					t.Fatal("deployment deleted twice")
				}
				deleted = true
				return body(200, ""), nil
			case "GET calm-cow-456.eu-west-1.convex.cloud/api/v1/get_canonical_urls":
				return body(200, `{"convexCloudUrl":"https://calm-cow-456.eu-west-1.convex.cloud","convexSiteUrl":"https://calm-cow-456.eu-west-1.convex.site"}`), nil
			case "GET calm-cow-456.eu-west-1.convex.cloud/api/v1/list_environment_variables":
				return body(200, `{"environmentVariables":{"UNRELATED":"unrelated-canary"}}`), nil
			case "POST calm-cow-456.eu-west-1.convex.cloud/api/v1/update_environment_variables":
				var request struct {
					Changes []map[string]string `json:"changes"`
				}
				if json.Unmarshal(data, &request) != nil || len(request.Changes) != 1 || request.Changes[0]["name"] != "SITE_URL" {
					t.Fatal("remote update broadened")
				}
				managed <- request.Changes[0]
				return body(200, ""), nil
			}
			t.Fatalf("unexpected provider operation %s %s://%s%s", r.Method, r.URL.Scheme, r.URL.Host, r.URL.Path)
			return nil, nil
		}))
		if err != nil {
			return nil, err
		}
		return api, nil
	}
}

func TestConvexAmbiguousCreateReconcilesExactReference(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	managed := make(chan map[string]string, 1)
	posts := 0
	factory := fakeConvexFactory(t, managed, true, &posts)
	_, err = provisionResources(t.Context(), r.store, lock, current, factory)
	errorCode(t, err, "E_PROVIDER_AMBIGUOUS")
	resources, err := lock.Resources(t.Context())
	if err != nil || resources[0].State != "unknown" {
		t.Fatal("ambiguous provider write not retained")
	}
	if _, err = provisionResources(t.Context(), r.store, lock, current, factory); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatal("unknown deployment was recreated instead of reconciled")
	}
	resources, err = lock.Resources(t.Context())
	if err != nil || resources[0].State != "configured" || resources[0].KeyGeneration != 1 {
		t.Fatal("exact resource not reconciled")
	}
	if key, err := lock.DeployKeyCredential(t.Context(), resources[0]); err != nil || key != "dev:calm-cow-456|convex-key" {
		t.Fatal("deploy key not persisted")
	}
}

func TestConvexProvisioningStagesAndPublishesExactBindings(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	identity, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	resourcesBefore, err := lock.Resources(t.Context())
	if err != nil || len(resourcesBefore) != 1 || resourcesBefore[0].State != "planned" {
		t.Fatal("resource intent missing")
	}
	managed := make(chan map[string]string, 1)
	factory := fakeConvexFactory(t, managed, false, nil)
	bindings, err := provisionResources(t.Context(), r.store, lock, current, factory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", bindings, bindings), "convex-key") {
		t.Fatal("lifecycle result formatting leaked credential")
	}
	if _, err := StageFilesWithBindings(t.Context(), r.store, r.client, lock, plan.Files, &bindings); err != nil {
		t.Fatal(err)
	}
	allocation, err := r.store.Allocation(t.Context(), plan.WorkspaceID)
	if err != nil || len(allocation.Endpoints) != 1 {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", fmt.Sprint(allocation.Endpoints[0].Port)))
	if err != nil {
		t.Fatal(err)
	}
	_, publishErr := PublishFiles(t.Context(), r.store, r.client, lock)
	errorCode(t, publishErr, "E_PORT_OCCUPIED")
	listener.Close()
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	backend, err := os.ReadFile(filepath.Join(identity.Path, "packages/backend", ".env.local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"EXISTING_UNMANAGED=kept\n", "CONVEX_DEPLOYMENT=dev:calm-cow-456\n", "CONVEX_DEPLOY_KEY=dev:calm-cow-456|convex-key\n"} {
		if !strings.Contains(string(backend), required) {
			t.Fatalf("backend image missing exact binding:\n%s", backend)
		}
	}
	frontend, err := os.ReadFile(filepath.Join(identity.Path, "apps", "web", ".env.local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"VITE_CONVEX_URL=https://calm-cow-456.eu-west-1.convex.cloud\n", "VITE_CONVEX_SITE_URL=https://calm-cow-456.eu-west-1.convex.site\n"} {
		if !strings.Contains(string(frontend), required) {
			t.Fatal("frontend output missing")
		}
	}
	update := <-managed
	wantURL := allocation.Endpoints[0].Scheme + "://" + net.JoinHostPort(allocation.Endpoints[0].Host, fmt.Sprint(allocation.Endpoints[0].Port))
	if update["name"] != "SITE_URL" || update["value"] != wantURL {
		t.Fatalf("bad remote env update: %v", update)
	}
	resources, err := lock.Resources(t.Context())
	if err != nil || resources[0].State != "configured" || resources[0].RemoteName != "calm-cow-456" || resources[0].KeyGeneration != 1 {
		t.Fatal("resource not configured")
	}
	secret, err := lock.DeployKeyCredential(t.Context(), resources[0])
	if err != nil || secret != "dev:calm-cow-456|convex-key" {
		t.Fatal("protected deploy key unavailable")
	}
	mode, err := os.Stat(filepath.Join(r.base, "state", "secrets", resources[0].CredentialID))
	if err != nil || mode.Mode().Perm() != 0600 {
		t.Fatal("deploy key object is not private")
	}
	if !strings.Contains(string(backend), "EXISTING_UNMANAGED=kept") || strings.Contains(string(frontend), "convex-key") {
		t.Fatal(func() string { return "unmanaged content lost or secret reached frontend" }())
	}
	secretID := resources[0].CredentialID
	destroyed, err := DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true, ProviderFactory: factory})
	if err != nil || destroyed.Workspace.State != "destroyed" {
		t.Fatalf("remote/local destroy failed: %v", err)
	}
	resources, err = lock.Resources(t.Context())
	if err != nil || resources[0].State != "deleted" {
		t.Fatal("remote resource not recorded deleted")
	}
	if _, err := os.Lstat(filepath.Join(r.base, "state", "secrets", secretID)); !os.IsNotExist(err) {
		t.Fatal("deployment key retained after destruction")
	}
	if _, err := os.Lstat(identity.Path); !os.IsNotExist(err) {
		t.Fatal("worktree retained after remote cleanup")
	}
}
