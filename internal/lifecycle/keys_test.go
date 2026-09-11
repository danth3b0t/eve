package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolationPreviewReportsOnlyExactManifestKeys(t *testing.T) {
	r := repositoryFixture(t)
	manifest := `version = 1
[workspace]
port_block_size = 4

[resources.backend]
provider = "convex"
path = "packages/backend"
project = "dev-team:m0"

[services.web]
path = "apps/web"
env_file = ".env.local"
port = "PORT"

[services.web.ports.hmr]
env = "HMR_PORT"
`
	if err := os.WriteFile(filepath.Join(r.root, "eve.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "interpolation keys fixture")
	preview, err := InterpolationPreviewForCheckout(t.Context(), r.client, r.root)
	if err != nil {
		t.Fatal(err)
	}
	var variables []string
	for _, variable := range preview.Keys {
		variables = append(variables, variable.Variable)
	}
	text := strings.Join(variables, "\n")
	for _, required := range []string{
		"${workspace.id}", "${workspace.slug}", "${workspace.branch}", "${workspace.port_base}",
		"${resources.backend.url}", "${resources.backend.site_url}", "${resources.backend.deployment}",
		"${services.web.port}", "${services.web.url}", "${services.web.ports.hmr.port}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("interpolation variable %s missing from %v", required, variables)
		}
	}
	for _, forbidden := range []string{"CONVEX_DEPLOY_KEY", "EVE_CONVEX_TOKEN", "dev-team:m0"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("project secret/config text leaked: %v", variables)
		}
	}
	if len(variables) != len(preview.Keys) || len(variables) != 12 {
		t.Fatalf("unexpected variable inventory: %v", variables)
	}
}
