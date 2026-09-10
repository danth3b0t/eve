//go:build linux || darwin

package files

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/resolve"

	"github.com/google/uuid"
)

func TestPublicationScalesWithDestinationsNotQuadratically(t *testing.T) {
	for _, count := range []int{10, 50, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { publicationSubprocessBound(t, count) })
	}
}

func publicationSubprocessBound(t *testing.T, count int) {
	t.Helper()
	f := newFixture(t)
	var manifest strings.Builder
	manifest.WriteString("version = 1\n")
	for i := range count {
		name := fmt.Sprintf("s%03d", i)
		manifest.WriteString(fmt.Sprintf("[services.%s]\npath = \"generated/%s\"\nenv_file = \".env\"\n[services.%s.env]\nOUTPUT = \"${workspace.id}\"\n", name, name, name))
		put(t, f.source, "generated/"+name+"/.keep", "")
	}
	f.put("eve.toml", manifest.String())
	f.put(".gitignore", "generated/**/*.env\n")
	f.commit()
	source, target := f.target("publication-scale", "")
	snapshot, err := Snapshot(t.Context(), f.g, source, target)
	if err != nil {
		t.Fatal(err)
	}
	targetIdentity := f.checkout(target)
	images, err := snapshot.Prepare(t.Context(), f.g, targetIdentity, resolve.Inputs{Workspace: resolve.Workspace{ID: "scale-workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != count {
		t.Fatalf("image count %d", len(images))
	}
	var ignoreCalls atomic.Int64
	realRunner := f.g.Runner
	wrapped := &git.Client{Runner: func(ctx context.Context, command git.Command) (git.Result, error) {
		if slices.Contains(command.Args, "check-ignore") {
			ignoreCalls.Add(1)
		}
		return realRunner(ctx, command)
	}}
	parsed, err := config.Parse(target.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	batch := make([]PublicationFile, 0, len(images))
	for _, image := range images {
		image.Mode = 0600 // publication private mode; lifecycle copies this from managed-file metadata
		batch = append(batch, PublicationFile{Image: image, Temp: TemporaryPath(image.Path, uuid.NewString())})
	}
	publisher, err := OpenPublisher(t.Context(), wrapped, targetIdentity, target.Branch, target.HeadOID, parsed, batch)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	for _, image := range images {
		if err := publisher.Publish(t.Context(), image.Path, func(domain.FileIdentity) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := publisher.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := ignoreCalls.Load(); calls > int64(4*count+4) {
		t.Fatalf("publication used %d ignore subprocesses for %d files", calls, count)
	}
}
