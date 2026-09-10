package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initDiscoveryFixture(t *testing.T, secondBackend bool) repository {
	t.Helper()
	r := repositoryFixture(t)
	if err := os.Remove(filepath.Join(r.root, "eve.toml")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":                    "apps/admin/.env.local\napps/web/.env.local\n",
		"apps/admin/package.json":       `{"scripts":{"dev":"vite"},"dependencies":{"vite":"latest"}}`,
		"apps/admin/vite.config.js":     `import { loadEnv } from "vite"; const env = loadEnv("development", process.cwd(), ""); export default { server: { strictPort: true, port: Number(env.PORT) } }`,
		"apps/admin/main.js":            `console.log("admin")`,
		"apps/web/package.json":         `{"scripts":{"dev":"vite"},"dependencies":{"vite":"latest"}}`,
		"apps/web/vite.config.js":       `import { loadEnv } from "vite"; const env = loadEnv("development", process.cwd(), ""); export default { server: { strictPort: true, port: Number(env.PORT) } }`,
		"apps/web/main.js":              `console.log(VITE_CONVEX_URL, VITE_CONVEX_SITE_URL)`,
		"packages/backend/package.json": `{"dependencies":{"convex":"latest"}}`,
		"packages/backend/convex.json":  `{}`,
		"packages/shared/report.js":     `console.log("VITE_CONVEX_URL")`,
	}
	for name, text := range files {
		path := filepath.Join(r.root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if secondBackend {
		if err := os.MkdirAll(filepath.Join(r.root, "packages", "other"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, "packages", "other", "convex.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, "packages", "other", "package.json"), []byte(`{"devDependencies":{"convex":"latest"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "discovery fixture")
	return r
}

func TestInitScopesEvidenceAndLeavesSiteURLUnresolved(t *testing.T) {
	r := initDiscoveryFixture(t, false)
	result, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0"})
	if err != nil {
		t.Logf("warnings: %v", result.Warnings)
		t.Fatal(err)
	}
	if len(result.Services) != 2 || result.Services[0].ID != "admin" || result.Services[1].ID != "web" {
		t.Fatalf("services nondeterministic or incomplete: %+v", result.Services)
	}
	if result.Services[0].PublicURL || result.Services[0].PublicSiteURL || !result.Services[1].PublicURL || !result.Services[1].PublicSiteURL {
		t.Fatal("public key evidence escaped service package scope")
	}
	manifest := result.ManifestText()
	for _, forbidden := range []string{"[resources.backend.env]", "\nSITE_URL = ", "[services.admin.env]"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("arbitrary relationship rendered:\n%s", manifest)
		}
	}
	if !strings.Contains(manifest, "[services.web.env]") || !strings.Contains(manifest, "VITE_CONVEX_URL") {
		t.Fatalf("web-scoped mapping missing:\n%s", manifest)
	}
	for range 32 {
		again, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0"})
		if err != nil || again.ManifestText() != manifest {
			t.Fatalf("discovery was not deterministic: %v", err)
		}
	}
}

func TestInitReportsMultipleConvexBackends(t *testing.T) {
	r := initDiscoveryFixture(t, true)
	_, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{Project: "dev-team:m0"})
	errorCode(t, err, "E_INIT_AMBIGUOUS")
	if !strings.Contains(err.Error(), "packages/backend") || !strings.Contains(err.Error(), "packages/other") {
		t.Fatalf("ambiguity omitted exact candidates: %v", err)
	}
}
