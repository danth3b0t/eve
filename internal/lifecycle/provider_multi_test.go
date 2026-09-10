package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eve/internal/provider/convex"
)

type multiFakeDeployment struct {
	ID        int64
	Name      string
	Reference string
	URL       string
	CreatedAt int64
	ExpiresAt int64
	KeyName   string
}

func multiFixture(t *testing.T) (repository, GitPlan) {
	t.Helper()
	r := repositoryFixture(t)
	manifest := `version = 1
[workspace]
port_block_size = 4
[resources.backend]
provider = "convex"
path = "packages/backend"
project = "dev-team:m0"
[resources.backend.env]
FIRST_ONLY = "first"
[resources.secondary]
provider = "convex"
path = "packages/secondary"
project = "dev-team:m0"
[resources.secondary.env]
SECOND_ONLY = "second"
[services.web]
path = "apps/web"
env_file = ".env.local"
port = "PORT"
[services.web.env]
VITE_ONE = "${resources.backend.url}"
VITE_TWO = "${resources.secondary.url}"
`
	for _, dir := range []string{"packages/backend", "packages/secondary", "apps/web"} {
		if err := os.MkdirAll(filepath.Join(r.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, dir, ".keep"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{".gitignore": "**/.env.local\n", "eve.toml": manifest} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "multi-resource fixture")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "multi-resource", "")
	if err != nil {
		t.Fatal(err)
	}
	return r, plan
}

func multiConvexFactory(t *testing.T, updates chan<- map[string]string) convexFactory {
	t.Helper()
	created := map[string]*multiFakeDeployment{}
	nextID := int64(100)
	return func(token string) (convexAdapter, error) {
		if token != "auth-holder-sentinel" {
			return nil, fmt.Errorf("unexpected token")
		}
		api, err := convex.New(token, convexTransport(func(r *http.Request) (*http.Response, error) {
			data := []byte(nil)
			if r.Body != nil {
				data, _ = io.ReadAll(r.Body)
			}
			route := r.Method + " " + r.URL.Host + r.URL.Path
			if route == "GET api.convex.dev/v1/teams/dev-team/projects/m0" {
				return body(200, `{"id":42,"teamId":7,"slug":"m0","teamSlug":"dev-team","devDeploymentName":"default-dev-123","prodDeploymentName":""}`), nil
			}
			if route == "GET api.convex.dev/v1/token_details" {
				return body(200, `{"type":"teamToken","teamId":7}`), nil
			}
			if route == "GET api.convex.dev/v1/deployments/default-dev-123" {
				return body(200, `{"id":10,"name":"default-dev-123","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":true,"reference":"dev/original","deploymentUrl":"https://default-dev-123.convex.cloud","createTime":1,"expiresAt":0}`), nil
			}
			if route == "GET api.convex.dev/v1/projects/42/deployment" {
				d := created[r.URL.Query().Get("reference")]
				if d == nil {
					return body(404, ""), nil
				}
				return body(200, fmt.Sprintf(`{"id":%d,"name":%q,"projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":%q,"createTime":%d,"expiresAt":%d}`, d.ID, d.Name, d.Reference, d.URL, d.CreatedAt, d.ExpiresAt)), nil
			}
			if route == "POST api.convex.dev/v1/projects/42/create_deployment" {
				var request convex.CreateDeploymentArgs
				if json.Unmarshal(data, &request) != nil || created[request.Reference] != nil {
					t.Fatalf("bad or repeated create: %s", data)
				}
				name := "calm-cow-456"
				if strings.Contains(request.Reference, "/secondary-") {
					name = "brave-fox-789"
				}
				nextID++
				d := &multiFakeDeployment{ID: nextID, Name: name, Reference: request.Reference, URL: "https://" + name + ".convex.cloud", CreatedAt: time.Now().UnixMilli(), ExpiresAt: request.ExpiresAt}
				created[request.Reference] = d
				return body(200, fmt.Sprintf(`{"id":%d,"name":%q,"projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":%q,"createTime":%d,"expiresAt":%d}`, d.ID, d.Name, d.Reference, d.URL, d.CreatedAt, d.ExpiresAt)), nil
			}
			if r.Method == "POST" && r.URL.Host == "api.convex.dev" && strings.HasPrefix(r.URL.Path, "/v1/deployments/") && strings.HasSuffix(r.URL.Path, "/create_deploy_key") {
				name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/deployments/"), "/create_deploy_key")
				d := findFakeDeployment(created, name)
				if d == nil {
					t.Fatalf("key for unknown deployment %s", name)
				}
				var request struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(data, &request) != nil || request.Name == "" {
					t.Fatal("bad key request")
				}
				d.KeyName = request.Name
				return body(200, fmt.Sprintf(`{"deployKey":"dev:%s|convex-key"}`, name)), nil
			}
			if r.Method == "GET" && r.URL.Host == "api.convex.dev" && strings.HasPrefix(r.URL.Path, "/v1/deployments/") && strings.HasSuffix(r.URL.Path, "/list_deploy_keys") {
				name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/deployments/"), "/list_deploy_keys")
				d := findFakeDeployment(created, name)
				if d == nil || d.KeyName == "" {
					return body(200, `[]`), nil
				}
				return body(200, fmt.Sprintf(`[{"id":9,"name":%q,"expiresAt":%d}]`, d.KeyName, d.ExpiresAt)), nil
			}
			if r.Method == "GET" && r.URL.Host == "api.convex.dev" && strings.HasPrefix(r.URL.Path, "/v1/deployments/") {
				name := strings.TrimPrefix(r.URL.Path, "/v1/deployments/")
				d := findFakeDeployment(created, name)
				if d == nil {
					return body(404, ""), nil
				}
				return body(200, fmt.Sprintf(`{"id":%d,"name":%q,"projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":%q,"deploymentUrl":%q,"createTime":%d,"expiresAt":%d}`, d.ID, d.Name, d.Reference, d.URL, d.CreatedAt, d.ExpiresAt)), nil
			}
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/api/v1/get_canonical_urls") {
				name := strings.TrimSuffix(r.URL.Host, ".convex.cloud")
				return body(200, fmt.Sprintf(`{"convexCloudUrl":"https://%s.convex.cloud","convexSiteUrl":"https://%s.convex.site"}`, name, name)), nil
			}
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/api/v1/list_environment_variables") {
				return body(200, `{"environmentVariables":{}}`), nil
			}
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/api/v1/update_environment_variables") {
				var request struct {
					Changes []map[string]string `json:"changes"`
				}
				if json.Unmarshal(data, &request) != nil || len(request.Changes) == 0 {
					t.Fatal("bad remote update")
				}
				if updates != nil {
					merged := map[string]string{"deployment": r.URL.Host}
					for _, change := range request.Changes {
						merged[change["name"]] = change["value"]
					}
					updates <- merged
				}
				return body(200, ""), nil
			}
			t.Fatalf("unexpected provider request %s %s", r.Method, r.URL)
			return nil, nil
		}))
		if err != nil {
			return nil, err
		}
		return api, nil
	}
}

func findFakeDeployment(created map[string]*multiFakeDeployment, name string) *multiFakeDeployment {
	for _, d := range created {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func TestTwoConvexResourcesProvisionBeforeResolution(t *testing.T) {
	r, plan := multiFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan map[string]string, 2)
	bindings, err := provisionResources(t.Context(), r.store, lock, current, multiConvexFactory(t, updates))
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings.outputs) != 2 || len(bindings.secrets) != 2 || bindings.outputs["backend"].URL == bindings.outputs["secondary"].URL {
		t.Fatalf("not every resource was output: %#v", bindings.outputs)
	}
	for range 2 {
		update := <-updates
		if (update["FIRST_ONLY"] == "first") == (update["SECOND_ONLY"] == "second") {
			t.Fatalf("resource env was not stage-scoped: %v", update)
		}
	}
	resources, err := lock.Resources(t.Context())
	if err != nil || len(resources) != 2 {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.State != "configured" || resource.RemoteName == "" || resource.AttemptStartedAtMS == 0 {
			t.Fatalf("resource did not reach exact configured identity: %+v", resource)
		}
	}
}
