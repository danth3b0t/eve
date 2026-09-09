package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/resolve"
)

const localManifest = "version = 1\n[services.web]\npath = \".\"\nenv_file = \".env.local\"\n[services.web.env]\nPUBLIC_NAME = \"prepared\"\n"
const canary = "private-source-canary-not-for-output"

type fixture struct {
	t            *testing.T
	base, source string
	g            *git.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	f := &fixture{t: t, base: base, source: filepath.Join(base, "source")}
	if err := os.Mkdir(f.source, 0700); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "init", "-b", "main")
	f.git(f.source, "config", "user.name", "Fixture")
	f.git(f.source, "config", "user.email", "fixture@example.invalid")
	f.put("eve.toml", localManifest)
	f.put(".gitignore", ".env.local\nlocal/\n")
	f.commit()
	f.g, err = git.New()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) git(root string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = root, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	data, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture Git %v: %v: %s", args, err, data)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func put(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) put(name, content string) { f.t.Helper(); put(f.t, f.source, name, content) }
func (f *fixture) commit() {
	f.t.Helper()
	f.git(f.source, "add", "-A")
	f.git(f.source, "commit", "-qm", "fixture revision")
}
func (f *fixture) target(branch, from string) (domain.GitIdentity, git.Target) {
	f.t.Helper()
	s, err := f.g.Inspect(f.t.Context(), f.source)
	if err != nil {
		f.t.Fatal(err)
	}
	target, err := f.g.Plan(f.t.Context(), s, branch, from)
	if err != nil {
		f.t.Fatal(err)
	}
	return s.Identity, target
}
func (f *fixture) plan() (*Plan, git.Target) {
	f.t.Helper()
	s, target := f.target("feature", "")
	p, err := Snapshot(f.t.Context(), f.g, s, target)
	if err != nil {
		f.t.Fatal(err)
	}
	return p, target
}
func (f *fixture) checkout(target git.Target) domain.GitIdentity {
	f.t.Helper()
	dest := filepath.Join(f.base, "target")
	if target.NewBranch {
		f.git(f.source, "worktree", "add", "-b", target.Branch, dest, target.HeadOID)
	} else {
		f.git(f.source, "worktree", "add", dest, target.Branch)
	}
	c, err := f.g.Inspect(f.t.Context(), dest)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Identity
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("diagnostic leaked source content")
	}
}

func TestNativeAndAdditionalInputsRemainPrivateAndUnpublished(t *testing.T) {
	f := newFixture(t)
	f.put("eve.toml", strings.Replace(localManifest, "[services.web]", "[workspace]\ncopy = [\"local/**/*\", \"tracked.txt\", \"missing-*.pem\"]\n[services.web]", 1))
	f.put("tracked.txt", "committed target code\n")
	f.commit()
	f.put("tracked.txt", "uncommitted source code must not be copied\n")
	original := "# keep CRLF\r\nPRIVATE='" + canary + "'\r\nexport PUBLIC_NAME = old # application\r\n"
	f.put(".env.local", original)
	f.put(".env.sibling", "unselected sibling\n")
	f.put("local/.hidden", "\x00opaque certificate bytes\xff")
	f.put("local/nested/config", "additional input")
	f.put("local/node_modules/do-not-copy", "installed")
	f.put("local/.convex/do-not-copy", "local backend")
	p, target := f.plan()
	if !slices.Equal(p.Report().UnmatchedGlobs, []string{"missing-*.pem"}) {
		t.Fatal("unmatched glob warning missing")
	}
	id := f.checkout(target)
	images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]Image)
	for _, image := range images {
		got[image.Path] = image
	}
	want := map[string]string{
		".env.local":          "# keep CRLF\r\nPRIVATE='" + canary + "'\r\nexport PUBLIC_NAME = prepared # application\r\n",
		"local/.hidden":       "\x00opaque certificate bytes\xff",
		"local/nested/config": "additional input",
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected image paths: %v", images)
	}
	for name, content := range want {
		if string(got[name].Data) != content || got[name].Mode != 0600 {
			t.Fatalf("image content/mode differs for %s", name)
		}
		if _, err := os.Lstat(filepath.Join(id.Path, name)); !os.IsNotExist(err) {
			t.Fatalf("planning published %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(id.Path, "tracked.txt"))
	if err != nil || string(data) != "committed target code\n" {
		t.Fatal("dirty source overwrote target code")
	}
	data, err = os.ReadFile(filepath.Join(f.source, ".env.local"))
	if err != nil || string(data) != original {
		t.Fatal("source native input changed")
	}
	for _, value := range []any{p, *p, images} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(canary)) || strings.Contains(fmt.Sprintf("%#v", value), canary) {
			t.Fatal("private images escaped metadata-only output")
		}
	}
}

func TestTrackedNativeUsesTargetNotDirtySource(t *testing.T) {
	f := newFixture(t)
	f.put("eve.toml", strings.Replace(localManifest, "[services.web.env]", "allow_tracked = true\n[services.web.env]", 1))
	f.put(".env.local", "# target\nPUBLIC_NAME=baseline\nKEEP=target\n")
	f.git(f.source, "add", "-f", ".env.local")
	f.commit()
	f.put(".env.local", "malformed dirty source \""+canary)
	p, target := f.plan()
	id := f.checkout(target)
	images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || !images[0].Tracked || string(images[0].Data) != "# target\nPUBLIC_NAME=prepared\nKEEP=target\n" || string(images[0].Preimage) != "# target\nPUBLIC_NAME=baseline\nKEEP=target\n" {
		t.Fatal("tracked target was not preserved as base/preimage")
	}
	if got := f.git(id.Path, "status", "--porcelain"); got != "" {
		t.Fatal("image generation edited tracked files")
	}
}

func TestTrackedConsentAndCredentialsFailDuringSnapshot(t *testing.T) {
	for _, kind := range []string{"missing-consent", "shared-consent", "credential"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			m := localManifest
			want := "E_TRACKED_FILE"
			if kind != "missing-consent" {
				m = strings.Replace(m, "[services.web.env]", "allow_tracked = true\n[services.web.env]", 1)
			}
			if kind == "shared-consent" {
				m += "[services.other]\npath = \".\"\nenv_file = \".env.local\"\n"
			}
			if kind == "credential" {
				m += "[resources.backend]\nprovider = \"convex\"\npath = \".\"\nproject = \"team:project\"\n"
				want = "E_TRACKED_CREDENTIAL_FILE"
			}
			f.put("eve.toml", m)
			f.put(".env.local", "PUBLIC_NAME=old\n")
			f.git(f.source, "add", "-f", ".env.local")
			f.commit()
			source, target := f.target("feature", "")
			p, err := Snapshot(t.Context(), f.g, source, target)
			requireCode(t, err, want)
			if p != nil {
				t.Fatal("failure exposed a partial file plan")
			}
		})
	}
}

func TestMissingTrackedTargetAndExactMissingCopy(t *testing.T) {
	f := newFixture(t)
	f.put("eve.toml", strings.Replace(localManifest, "[services.web]", "[workspace]\ncopy = [\"config.txt\"]\n[services.web]", 1))
	f.commit()
	old := f.git(f.source, "rev-parse", "HEAD")
	source, target := f.target("missing", "")
	_, err := Snapshot(t.Context(), f.g, source, target)
	requireCode(t, err, "E_COPY_MISSING")
	f.put("config.txt", "tracked source")
	f.commit()
	source, target = f.target("older", old)
	_, err = Snapshot(t.Context(), f.g, source, target)
	requireCode(t, err, "E_COPY_TARGET_MISSING")
	// A deleted working-copy file remains tracked; the target still supplies it.
	if err := os.Remove(filepath.Join(f.source, "config.txt")); err != nil {
		t.Fatal(err)
	}
	source, target = f.target("valid", "")
	if _, err := Snapshot(t.Context(), f.g, source, target); err != nil {
		t.Fatal(err)
	}
}

func TestTargetIgnorePolicyAndLateFileChanges(t *testing.T) {
	for _, kind := range []string{"target-rule", "global-rule", "existing", "tracked-late", "hardlink", "parent-link"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.put("config/.keep", "directory")
			f.commit()
			var p *Plan
			var target git.Target
			if kind == "target-rule" {
				f.put(".gitignore", "local/\n")
				f.commit()
				old := f.git(f.source, "rev-parse", "HEAD")
				f.put(".gitignore", ".env.local\nlocal/\n")
				f.commit()
				source, planned := f.target("feature", old)
				target = planned
				var err error
				p, err = Snapshot(t.Context(), f.g, source, target)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if kind == "global-rule" {
					f.put(".gitignore", "local/\n")
					f.commit()
					ignore := filepath.Join(f.base, "global-ignore")
					if err := os.WriteFile(ignore, []byte(".env.local\n"), 0600); err != nil {
						t.Fatal(err)
					}
					f.git(f.source, "config", "--global", "core.excludesFile", ignore)
				}
				if kind == "parent-link" {
					f.put("eve.toml", strings.Replace(localManifest, ".env.local", "config/.env.local", 1))
					f.commit()
				}
				p, target = f.plan()
			}
			id := f.checkout(target)
			want := "E_FILE_CHANGED"
			switch kind {
			case "target-rule":
				want = "E_IGNORE_MISSING"
			case "global-rule":
				want = ""
			case "existing":
				put(t, id.Path, ".env.local", "do not replace")
			case "tracked-late":
				put(t, id.Path, ".env.local", "tracked now")
				f.git(id.Path, "add", "-f", ".env.local")
			case "hardlink":
				outside := filepath.Join(f.base, "outside")
				if err := os.WriteFile(outside, []byte(canary), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, filepath.Join(id.Path, ".env.local")); err != nil {
					t.Fatal(err)
				}
				want = "E_HARDLINK"
			case "parent-link":
				if err := os.Rename(filepath.Join(id.Path, "config"), filepath.Join(f.base, "saved-config")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.source, "config"), filepath.Join(id.Path, "config")); err != nil {
					t.Fatal(err)
				}
				want = "E_SYMLINK"
			}
			images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
			if want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, want)
			if images != nil {
				t.Fatal("unsafe target produced partial images")
			}
			if want == "E_IGNORE_MISSING" && !strings.Contains(err.Error(), `/.env.local`) {
				t.Fatal("missing exact ignore recommendation")
			}
		})
	}
}

func TestSourceSnapshotDetectsContentAndSelectionRaces(t *testing.T) {
	for _, kind := range []string{"during-read", "after-plan", "new-match"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.put("eve.toml", strings.Replace(localManifest, "[services.web]", "[workspace]\ncopy = [\"local/*.txt\"]\n[services.web]", 1))
			f.commit()
			f.put(".env.local", "PRIVATE=old\n")
			before, err := os.Stat(filepath.Join(f.source, ".env.local"))
			if err != nil {
				t.Fatal(err)
			}
			change := func() {
				f.put(".env.local", "PRIVATE=new\n")
				if err := os.Chtimes(filepath.Join(f.source, ".env.local"), before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "during-read" {
				runner, calls := f.g.Runner, 0
				f.g.Runner = func(ctx context.Context, cmd git.Command) (git.Result, error) {
					result, err := runner(ctx, cmd)
					if slices.Contains(cmd.Args, "--cached") {
						calls++
						if calls == 2 {
							change()
						}
					}
					return result, err
				}
				source, target := f.target("feature", "")
				p, err := Snapshot(t.Context(), f.g, source, target)
				requireCode(t, err, "E_FILE_CHANGED")
				if p != nil {
					t.Fatal("inconsistent source snapshot accepted")
				}
				return
			}
			p, target := f.plan()
			id := f.checkout(target)
			if kind == "after-plan" {
				change()
			} else {
				f.put("local/new.txt", "newly selected")
			}
			images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
			requireCode(t, err, "E_FILE_CHANGED")
			if images != nil {
				t.Fatal("stale snapshot generated images")
			}
		})
	}
}
