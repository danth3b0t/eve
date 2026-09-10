package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvexOnlyInitAndReviewedUpdate(t *testing.T) {
	base, root := fixture(t)
	if err := os.Remove(filepath.Join(root, "eve.toml")); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"packages/backend/package.json": `{"dependencies":{"convex":"latest"}}`,
		"packages/backend/convex.json":  `{}`,
		".gitignore":                    "env/generated.env\n**/.env.local\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "backend-only convex package")

	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	code, stdout, _, _ := command(t, binary, root, base, "init", "--convex", "--write", "--yes", "--json", "--project", "dev-team:m0")
	if code != 0 || !strings.Contains(string(stdout), `"has_convex_backend":true`) || !strings.Contains(string(stdout), `"written":true`) || strings.Contains(string(stdout), `"services":[`) {
		t.Fatalf("backend-only init: %d %s", code, stdout)
	}
	runGit(t, root, "add", "eve.toml")
	runGit(t, root, "commit", "-qm", "review backend-only manifest")

	files := map[string]string{
		"apps/web/package.json":   `{"scripts":{"dev":"vite"},"dependencies":{"vite":"latest"}}`,
		"apps/web/vite.config.js": `import { loadEnv } from "vite"; const env = loadEnv("development", process.cwd(), ""); export default { server: { strictPort: true, port: Number(env.PORT) } }`,
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "add reviewed vite consumer")
	if err := os.WriteFile(filepath.Join(root, "apps/web/.env.local"), []byte("CUSTOM_BACKEND=https://stale-project.convex.cloud\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "packages/backend/.env.local"), []byte("CONVEX_URL=https://stale-project.convex.cloud\n"), 0600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _, _ = command(t, binary, root, base, "init", "--update", "--write", "--yes", "--json", "--convex")
	if code != 0 || !strings.Contains(string(stdout), `"updated":true`) || !strings.Contains(string(stdout), `CUSTOM_BACKEND-`) {
		t.Fatalf("incremental init update: %d %s", code, stdout)
	}
	data, err := os.ReadFile(filepath.Join(root, "eve.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `CUSTOM_BACKEND = "${resources.backend.url}"`) || !strings.Contains(text, `[resources.backend]`) || strings.Contains(text, "stale-project") {
		t.Fatalf("updated manifest lost/add relationship:\n%s", text)
	}
	if info, err := os.Stat(filepath.Join(root, "eve.toml")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("updated manifest mode")
	}
	runGit(t, root, "add", "eve.toml")
	runGit(t, root, "commit", "-qm", "review updated consumer binding")
	code, stdout, _, _ = command(t, binary, root, base, "plan", "--json", "after-update")
	if code != 0 || !strings.Contains(string(stdout), `"service":"web"`) || !strings.Contains(string(stdout), `CUSTOM_BACKEND`) {
		t.Fatalf("plan after imported update: %d %s", code, stdout)
	}
}
