package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/config"
)

func backendOnlyFixture(t *testing.T) repository {
	t.Helper()
	r := repositoryFixture(t)
	if err := os.Remove(filepath.Join(r.root, "eve.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.root, "packages/backend"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"packages/backend/package.json": `{"dependencies":{"convex":"1.0.0"}}`,
		"packages/backend/convex.json":  `{}`,
	} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "backend-only discovery")
	return r
}

func TestConvexOnlyInitDoesNotRequireAWebService(t *testing.T) {
	r := backendOnlyFixture(t)
	result, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0", Convex: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services) != 0 || !result.HasConvexBackend {
		t.Fatalf("backend-only proposal: %+v", result)
	}
	manifest, err := config.Parse(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	backend := manifest.Resources["backend"]
	if len(manifest.Services) != 0 || len(manifest.Resources) != 1 || backend.Path != "packages/backend" || backend.Project != "dev-team:m0" || len(backend.Env) != 0 {
		t.Fatalf("resource-only manifest: %#v", manifest)
	}
	if len(manifest.Endpoints()) != 0 {
		t.Fatal("backend-only onboarding invented a listener")
	}
}

func TestInitImportsExactLocalConvexBindingsWithoutValues(t *testing.T) {
	r := initDiscoveryFixture(t, false)
	local := "VITE_CONVEX_URL=https://existing-a.convex.cloud\nVITE_CONVEX_SITE_URL=https://existing-a.convex.site\nCUSTOM_BACKEND=https://existing-offline.convex.cloud\nDO_NOT_EXPORT_THIS=secret-local-value\n"
	if err := os.WriteFile(filepath.Join(r.root, "apps/web/.env.local"), []byte(local), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0", Convex: true})
	if err != nil {
		t.Fatal(err)
	}
	text := result.ManifestText()
	for _, required := range []string{
		"VITE_CONVEX_URL = \"${resources.backend.url}\"",
		"VITE_CONVEX_SITE_URL = \"${resources.backend.site_url}\"",
		"CUSTOM_BACKEND = \"${resources.backend.url}\"",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("imported binding missing %q:\n%s", required, text)
		}
	}
	for _, secret := range []string{"existing-a.convex", "existing-offline.convex", "secret-local-value", "DO_NOT_EXPORT_THIS"} {
		if strings.Contains(text, secret) {
			t.Fatalf("source dotenv/value leaked into manifest:\n%s", text)
		}
	}
	if _, err := config.Parse(result.Manifest); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitBackendPathAndSiteURLSelection(t *testing.T) {
	r := initDiscoveryFixture(t, true)
	result, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0", Convex: true, BackendPath: "packages/backend", SiteURLService: "web"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.Parse(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Resources["backend"].Path != "packages/backend" || manifest.Resources["backend"].Env["SITE_URL"] != "${services.web.url}" {
		t.Fatalf("explicit resource/return URL not selected: %#v", manifest.Resources)
	}
	if err == nil && result.ManifestText() == "" {
		t.Fatal("empty manifest")
	}
	if _, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0", BackendPath: "packages/missing"}); err == nil {
		t.Fatal("missing explicit backend accepted")
	}
}

func TestInitUpdatePreservesExistingManifestAndAddsReviewedRelations(t *testing.T) {
	r := initDiscoveryFixture(t, false)
	existing := `version = 1

[services.legacy]
path = "apps/legacy"
env_file = ".env.local"
port = "LEGACY_PORT"

[services.legacy.env]
CUSTOM_KEEP = "preserved"
`
	if err := os.MkdirAll(filepath.Join(r.root, "apps/legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "eve.toml"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "existing custom manifest")
	local := "CUSTOM_BACKEND=https://existing-offline.convex.cloud\n"
	if err := os.WriteFile(filepath.Join(r.root, "apps/web/.env.local"), []byte(local), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0", Convex: true, Update: true})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := config.Parse(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Services["legacy"].Env["CUSTOM_KEEP"] != "preserved" || manifest.Services["web"].Env["CUSTOM_BACKEND"] != "${resources.backend.url}" || manifest.Resources["backend"].Project != "dev-team:m0" {
		t.Fatalf("update did not preserve/add exact relations: %#v", manifest)
	}
	if strings.Contains(result.ManifestText(), "existing-offline") {
		t.Fatal("old deployment value retained in update")
	}
}
