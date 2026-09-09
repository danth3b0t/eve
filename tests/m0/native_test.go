//go:build linux || darwin

// Package m0 contains opt-in engineering probes, not an EVE implementation.
package m0

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const bunVersion = "1.4.2"

// Only fixture dependencies and these three executables are visible to the
// ordinary launch command. No ambient tokens, global Convex login, EVE or mise.
type fixture struct {
	root string
	env  []string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	must(t, os.Mkdir(bin, 0700))
	for _, name := range []string{"bun", "node", "git"} {
		path, err := exec.LookPath(name)
		must(t, err)
		must(t, os.Symlink(path, filepath.Join(bin, name)))
	}
	home := filepath.Join(base, "home")
	must(t, os.Mkdir(home, 0700))
	f := fixture{
		root: filepath.Join(base, "source"),
		env: []string{
			"PATH=" + bin, "HOME=" + home, "XDG_CONFIG_HOME=" + home,
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=M0", "GIT_AUTHOR_EMAIL=m0@example.invalid",
			"GIT_COMMITTER_NAME=M0", "GIT_COMMITTER_EMAIL=m0@example.invalid",
			"TURBO_TELEMETRY_DISABLED=1", "DO_NOT_TRACK=1", "NO_COLOR=1",
		},
	}
	must(t, os.Mkdir(f.root, 0700))
	if got := strings.TrimSpace(f.run(t, "bun", "--version")); got != bunVersion {
		t.Fatalf("fixture requires Bun %s, got %s", bunVersion, got)
	}
	t.Logf("Bun %s; Node %s", bunVersion, strings.TrimSpace(f.run(t, "node", "--version")))
	src, err := filepath.Abs("../../testdata/native-launch")
	must(t, err)
	must(t, filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "node_modules" || entry.Name() == ".turbo" || entry.Name() == "_generated" {
			return filepath.SkipDir
		}
		if entry.Name() == ".env.local" {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(f.root, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}))
	f.run(t, "git", "init", "-b", "main")
	f.run(t, "git", "add", ".")
	f.run(t, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "Native baseline before manual configuration")
	f.install(t)
	return f
}

func (f fixture) command(name string, args ...string) *exec.Cmd {
	// exec.LookPath normally uses the harness's PATH, not Cmd.Env.
	bin := strings.TrimPrefix(f.env[0], "PATH=")
	cmd := exec.Command(filepath.Join(bin, name), args...)
	cmd.Dir, cmd.Env = f.root, f.env
	return cmd
}

func (f fixture) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := f.command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	must(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			// Never include arbitrary subprocess output: live CLI errors may
			// contain credential values. The executable and action suffice.
			t.Fatalf("%s %s failed: %v (output withheld)", name, args[0], err)
		}
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s timed out", name)
	}
	return out.String()
}

func (f fixture) install(t *testing.T) {
	// Dependency installation is the project's ordinary procedure, not EVE.
	f.run(t, "bun", "install", "--frozen-lockfile")
}

func (f fixture) worktree(t *testing.T, branch string) fixture {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.root), branch)
	f.run(t, "git", "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", branch, path, "HEAD")
	w := fixture{root: path, env: f.env}
	w.install(t)
	return w
}

func (f fixture) unchanged(t *testing.T) {
	t.Helper()
	if f.run(t, "git", "status", "--porcelain", "--untracked-files=all") != "" {
		t.Fatal("fixture source, scripts, launch configuration, or lockfile changed")
	}
}

func (f fixture) write(t *testing.T, path, value string) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(f.root, path), []byte(value), 0600))
}

type nativeValues struct {
	ports   [2]int
	url     string
	siteURL string
}

func manualValues(t *testing.T, label string) nativeValues {
	return nativeValues{
		ports: freePorts(t), url: "https://" + label + ".convex.cloud",
		siteURL: "https://" + label + ".convex.site",
	}
}

func (f fixture) configureFrontends(t *testing.T, v nativeValues) {
	for i, app := range []string{"web", "admin"} {
		f.write(t, "apps/"+app+"/.env.local", fmt.Sprintf(
			"# Manually prepared M0 native configuration\nPORT=%d\nVITE_CONVEX_URL=%s\nVITE_CONVEX_SITE_URL=%s\n",
			v.ports[i], v.url, v.siteURL))
	}
}

func freePorts(t *testing.T) [2]int {
	t.Helper()
	var ports [2]int
	for i := range ports {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		must(t, err)
		defer listener.Close()
		ports[i] = listener.Addr().(*net.TCPAddr).Port
	}
	return ports
}

type running struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // read only after done is closed
	once sync.Once
}

func (f fixture) start(t *testing.T, args ...string) *running {
	t.Helper()
	p := &running{cmd: f.command("bun", args...), done: make(chan struct{})}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.cmd.Stdout, p.cmd.Stderr = io.Discard, io.Discard
	must(t, p.cmd.Start())
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(p.stop)
	return p
}

func (p *running) stop() {
	p.once.Do(func() {
		localHTTP.CloseIdleConnections() // client initiates closure before the server disappears
		// Process management belongs exclusively to this test harness.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
		// Some child pipelines can outlive a graceful parent exit. Harness cleanup
		// must not leave a listener behind merely because that exit completed first.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	})
}

func waitFor(t *testing.T, p *running, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("ordinary launch exited before runtime verification: %v (output withheld)", p.err)
		default:
		}
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("runtime verification timed out; file contents are not runtime evidence")
}

var localHTTP = &http.Client{Timeout: time.Second}

// Inspect Vite's actual browser module served by the listener, not .env files
// or a subprocess environment dump. Browser interaction is a separate gate.
func browserConfig(port int, v nativeValues) bool {
	resp, err := localHTTP.Get(fmt.Sprintf("http://127.0.0.1:%d/main.js", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 {
		return false
	}
	prefix := "import.meta.env = "
	_, text, ok := strings.Cut(string(body), prefix)
	if !ok {
		return false
	}
	var env map[string]any
	if json.NewDecoder(strings.NewReader(text)).Decode(&env) != nil {
		return false
	}
	return env["VITE_CONVEX_URL"] == v.url && env["VITE_CONVEX_SITE_URL"] == v.siteURL && env["CONVEX_DEPLOY_KEY"] == nil
}

func assertFrontends(t *testing.T, p *running, v nativeValues) {
	t.Helper()
	waitFor(t, p, func() bool { return browserConfig(v.ports[0], v) && browserConfig(v.ports[1], v) })
}

func TestNativeFrontendSlice(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" {
		t.Skip("set EVE_M0_NATIVE=1; requires pinned Bun, Node, Git and package downloads")
	}
	f := newFixture(t)
	args := []string{"run", "dev", "--filter=@eve-m0/web", "--filter=@eve-m0/admin"}
	baseline := manualValues(t, "baseline")
	f.configureFrontends(t, baseline)
	p := f.start(t, args...)
	assertFrontends(t, p, baseline)
	p.stop()
	f.unchanged(t)

	a, b := f.worktree(t, "workspace-a"), f.worktree(t, "workspace-b")
	va := manualValues(t, "workspace-a")
	a.configureFrontends(t, va)
	pa := a.start(t, args...)
	assertFrontends(t, pa, va)
	vb := manualValues(t, "workspace-b")
	b.configureFrontends(t, vb)
	pb := b.start(t, args...)
	assertFrontends(t, pb, vb)
	a.unchanged(t)
	b.unchanged(t)
	pa.stop()
	f.run(t, "git", "-c", "core.hooksPath=/dev/null", "worktree", "remove", a.root)
	assertFrontends(t, pb, vb)
	pb.stop()
	f.unchanged(t)

	t.Run("package boundary", func(t *testing.T) {
		leaf := fixture{root: filepath.Join(f.root, "apps/web"), env: f.env}
		p := leaf.start(t, "run", "dev")
		defer p.stop()
		waitFor(t, p, func() bool { return browserConfig(baseline.ports[0], baseline) })
	})

	t.Run("root dotenv is not inherited by external scripts on pinned Bun", func(t *testing.T) {
		root := manualValues(t, "root-override")
		f.write(t, ".env.local", fmt.Sprintf("PORT=%d\nVITE_CONVEX_URL=%s\nVITE_CONVEX_SITE_URL=%s\n", root.ports[0], root.url, root.siteURL))
		defer os.Remove(filepath.Join(f.root, ".env.local"))
		p := f.start(t, "run", "dev", "--filter=@eve-m0/web")
		defer p.stop()
		waitFor(t, p, func() bool { return browserConfig(baseline.ports[0], baseline) })
		if browserConfig(root.ports[0], root) {
			t.Fatal("unexpected root override; revisit the pinned loader contract")
		}
	})

	t.Run("ambient variables mask native files", func(t *testing.T) {
		ambient := manualValues(t, "ambient-override")
		override := fixture{root: f.root, env: append(append([]string{}, f.env...),
			fmt.Sprintf("PORT=%d", ambient.ports[0]), "VITE_CONVEX_URL="+ambient.url, "VITE_CONVEX_SITE_URL="+ambient.siteURL)}
		p := override.start(t, "run", "dev", "--filter=@eve-m0/web")
		defer p.stop()
		waitFor(t, p, func() bool { return browserConfig(ambient.ports[0], ambient) })
	})

	t.Run("occupied configured port does not silently fall back", func(t *testing.T) {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", baseline.ports[0]))
		must(t, err)
		defer listener.Close()
		p := f.start(t, "run", "dev", "--filter=@eve-m0/web")
		defer p.stop()
		select {
		case <-p.done:
			if p.err == nil {
				t.Fatal("occupied port must fail startup")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("launcher did not reject occupied configured port")
		}
	})
	f.unchanged(t)
}

func TestUnsupportedBunScriptLoader(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" {
		t.Skip("set EVE_M0_NATIVE=1")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:5173")
	if err != nil {
		t.Skip("unsupported fixture's fixed fallback port 5173 is occupied")
	}
	listener.Close()
	f := newFixture(t)
	config, err := os.ReadFile("../../testdata/unsupported-launch/vite.config.js")
	must(t, err)
	f.write(t, "apps/web/vite.config.js", string(config))
	f.run(t, "git", "add", ".")
	f.run(t, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "Unsupported process.env-only baseline")
	v := manualValues(t, "unsupported")
	f.configureFrontends(t, v)
	p := f.start(t, "run", "dev", "--filter=@eve-m0/web")
	defer p.stop()
	waitFor(t, p, func() bool { return browserConfig(5173, v) })
	if browserConfig(v.ports[0], v) {
		t.Fatal("candidate unexpectedly consumed PORT; revisit unsupported diagnostic")
	}
	f.unchanged(t)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
