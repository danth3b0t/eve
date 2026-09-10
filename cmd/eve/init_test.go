package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesExactlyOneRootManifestFromSubdirectory(t *testing.T) {
	base, root := fixture(t)
	if err := os.Remove(filepath.Join(root, "eve.toml")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":                         "apps/web/.env.local\n",
		"apps/web/package.json":              `{"scripts":{"dev":"vite"},"dependencies":{"vite":"latest"}}`,
		"apps/web/vite.config.js":            `import { loadEnv } from "vite"; const env = loadEnv("development", process.cwd(), ""); export default { server: { strictPort: true, port: Number(env.PORT) } }`,
		"apps/web/main.js":                   `console.log(VITE_CONVEX_URL)`,
		"packages/backend/package.json":      `{"dependencies":{"convex":"latest"}}`,
		"packages/backend/convex.json":       `{}`,
		"packages/backend/convex/backend.js": `export default {}`,
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
	runGit(t, root, "commit", "-qm", "init fixture")
	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	cmd := exec.Command(binary, "init", "--write", "--yes", "--project", "dev-team:m0", "--json")
	cmd.Dir = filepath.Join(root, "apps", "web")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(base, "home"), "XDG_CONFIG_HOME=" + filepath.Join(base, "config"), "XDG_STATE_HOME=" + filepath.Join(base, "xdg"), "EVE_STATE_DIR=" + filepath.Join(base, "state"), "GIT_CONFIG_NOSYSTEM=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("init from subdirectory: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"written":true`) {
		t.Fatalf("init output: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "eve.toml")); err != nil {
		t.Fatal("repository root manifest missing")
	}
	if _, err := os.Lstat(filepath.Join(root, "apps", "web", "eve.toml")); !os.IsNotExist(err) {
		t.Fatal("manifest also written in invoking service")
	}
}
