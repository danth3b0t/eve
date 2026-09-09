package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/platform"
	"eve/internal/resolve"
	"golang.org/x/sys/unix"
)

func TestGlobVocabulary(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"*.env", ".hidden.env", true}, {"*.env", "nested/a.env", false},
		{"**/*.env", "root.env", true}, {"**/*.env", "a/b/.hidden.env", true},
		{"local/*/?.txt", "local/a/é.txt", true}, {"local/*/?.txt", "local/a/ab.txt", false},
		{"local/**", "local/a/b", true}, {"*.ENV", "a.env", false},
		{"*.env", "line\nbreak.env", true},
	} {
		re, _, err := glob(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if re.MatchString(tc.name) != tc.want {
			t.Fatalf("glob %q matching %q", tc.pattern, tc.name)
		}
	}
	for _, pattern := range []string{"../*", "a*/../file", "**/.git/*", "local//*.env"} {
		if _, _, err := glob(pattern); err == nil {
			t.Fatalf("unsafe pattern accepted: %q", pattern)
		}
	}
}

func TestSourceLinksAndNonregularFiles(t *testing.T) {
	for _, kind := range []string{"leaf-link", "erased-link", "fifo", "directory", "committed-link"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.put("config/.keep", "directory")
			if kind == "erased-link" || kind == "committed-link" {
				f.put("eve.toml", strings.Replace(localManifest, ".env.local", "config/../.env.local", 1))
			}
			if kind == "committed-link" {
				if err := os.Rename(filepath.Join(f.source, "config"), filepath.Join(f.base, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.base, filepath.Join(f.source, "config")); err != nil {
					t.Fatal(err)
				}
			}
			f.commit()
			want := "E_SYMLINK"
			switch kind {
			case "leaf-link":
				if err := os.Symlink(filepath.Join(f.base, "outside"), filepath.Join(f.source, ".env.local")); err != nil {
					t.Fatal(err)
				}
			case "erased-link":
				if err := os.Rename(filepath.Join(f.source, "config"), filepath.Join(f.base, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.base, filepath.Join(f.source, "config")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(filepath.Join(f.source, ".env.local"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "E_FILE_TYPE"
			case "directory":
				if err := os.Mkdir(filepath.Join(f.source, ".env.local"), 0700); err != nil {
					t.Fatal(err)
				}
				want = "E_FILE_TYPE"
			}
			source, target := f.target("feature", "")
			p, err := Snapshot(t.Context(), f.g, source, target)
			requireCode(t, err, want)
			if p != nil {
				t.Fatal("unsafe source returned partial data")
			}
		})
	}
}

func TestCopyBoundsAndCancelledReads(t *testing.T) {
	location, identity, err := platform.DirectoryIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := openRoot(location, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer r.fs.Close()
	index := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		index[fmt.Sprintf("file-%05d.cfg", i)] = true
	}
	if _, err := selectCopies(t.Context(), r, []string{"*.cfg"}, index, make(map[string][]string)); err != nil {
		t.Fatal(err)
	}
	index["one-too-many.cfg"] = true
	_, err = selectCopies(t.Context(), r, []string{"*.cfg"}, index, make(map[string][]string))
	requireCode(t, err, "E_COPY_LIMIT")
	put(t, location, "exact", "abc")
	data, err := r.read(t.Context(), "exact", 3, false)
	if err != nil || string(data.data) != "abc" {
		t.Fatalf("inclusive byte limit: %v", err)
	}
	_, err = r.read(t.Context(), "exact", 2, false)
	requireCode(t, err, "E_FILE_LIMIT")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.read(ctx, "exact", 3, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOversizedCopyRefusedBeforeReading(t *testing.T) {
	f := newFixture(t)
	f.put("eve.toml", strings.Replace(localManifest, "[services.web]", "[workspace]\ncopy = [\"local/big\"]\n[services.web]", 1))
	f.commit()
	f.put("local/big", "")
	if err := os.Truncate(filepath.Join(f.source, "local/big"), (256<<20)+1); err != nil {
		t.Fatal(err)
	}
	source, target := f.target("feature", "")
	p, err := Snapshot(t.Context(), f.g, source, target)
	requireCode(t, err, "E_FILE_LIMIT")
	if p != nil {
		t.Fatal("oversized source produced a plan")
	}
}

func TestAmbiguousMissingAliasesAndFileParentCollision(t *testing.T) {
	for _, pair := range [][2]string{{"local/Env", "local/env"}, {"local/café", "local/cafe\u0301"}, {"local/config", "local/config/file"}, {"local/config", "local/config-other"}} {
		t.Run(pair[1], func(t *testing.T) {
			f := newFixture(t)
			m := "version=1\n[services.one]\npath=\".\"\nenv_file=" + fmt.Sprintf("%q", pair[0]) + "\n[services.two]\npath=\".\"\nenv_file=" + fmt.Sprintf("%q", pair[1]) + "\n"
			if strings.HasSuffix(pair[1], "/file") {
				m += "[services.between]\npath=\".\"\nenv_file=\"local/config-other\"\n"
			}
			f.put("eve.toml", m)
			f.commit()
			source, target := f.target("feature", "")
			p, err := Snapshot(t.Context(), f.g, source, target)
			if strings.HasSuffix(pair[1], "/file") {
				requireCode(t, err, "E_NATIVE_PATH_CONFLICT")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			id := f.checkout(target)
			images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
			if strings.HasSuffix(pair[1], "-other") {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, "E_PATH_ALIAS")
			if images != nil {
				t.Fatal("ambiguous aliases produced images")
			}
		})
	}
}

func TestIgnoreLiteralNamesAndNoFalseAbsence(t *testing.T) {
	f := newFixture(t)
	f.put(".gitignore", "/:literal.env\n/star\\*.env\n/bracket\\[x\\].env\n/.env.local\n")
	f.commit()
	for _, name := range []string{":literal.env", "star*.env", "bracket[x].env"} {
		ignored, err := f.g.Ignored(t.Context(), f.source, name)
		if err != nil || !ignored {
			t.Fatalf("literal ignore %q: %v", name, err)
		}
	}
	if ignored, err := f.g.Ignored(t.Context(), f.source, "not-ignored"); err != nil || ignored {
		t.Fatalf("not ignored: %v", err)
	}
	if ignored, err := f.g.Ignored(t.Context(), filepath.Join(f.base, "missing"), ".env.local"); err == nil || ignored {
		t.Fatal("Git failure became false absence")
	}
}

func TestResourceImagesCannotPublishSourceSelectors(t *testing.T) {
	f := newFixture(t)
	f.put("eve.toml", "version=1\n[resources.backend]\nprovider=\"convex\"\npath=\".\"\nproject=\"team:project\"\n")
	f.commit()
	f.put(".env.local", "CONVEX_DEPLOYMENT=dev:original\nCONVEX_DEPLOY_KEY="+canary+"\n")
	p, target := f.plan()
	id := f.checkout(target)
	images, err := p.Prepare(t.Context(), f.g, id, resolve.Inputs{})
	requireCode(t, err, "E_PROVIDER_BINDING_PENDING")
	if images != nil {
		t.Fatal("source provider selectors escaped as final images")
	}
	if _, err := os.Stat(filepath.Join(id.Path, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("source selectors were published")
	}
}
