package files

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path"
	"slices"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/git"
)

type File struct {
	Path, Origin    string // local, target, or new
	Native, Tracked bool
	Keys            []string
}

type Report struct {
	Files          []File
	UnmatchedGlobs []string
	CopyBytes      int64
}

type input struct {
	file          File
	original      []string
	sourceTracked bool
	local         snapshot
	binding       *binding
}

// Plan owns private in-memory snapshots. Report/JSON contain names and metadata
// only. It is not a durable journal, write capability, or complete launch plan.
type Plan struct {
	source   domain.GitIdentity
	target   git.Target
	manifest *config.Manifest
	inputs   map[string]input
	report   Report
}

func (p *Plan) Report() Report {
	r := Report{CopyBytes: p.report.CopyBytes, UnmatchedGlobs: slices.Clone(p.report.UnmatchedGlobs)}
	for _, file := range p.report.Files {
		file.Keys = slices.Clone(file.Keys)
		r.Files = append(r.Files, file)
	}
	return r
}
func (p Plan) MarshalJSON() ([]byte, error) { return json.Marshal(p.Report()) }
func (p Plan) String() string               { data, _ := p.MarshalJSON(); return string(data) }
func (p Plan) GoString() string             { return p.String() }

// Snapshot performs bounded read-only source/committed-target preflight before
// intent/allocation/Git creation. Actual checkout ignore/tracking/identity must
// still pass Prepare; Git cannot check a different commit's ignore rules here.
func Snapshot(ctx context.Context, g *git.Client, source domain.GitIdentity, target git.Target) (*Plan, error) {
	checkout, err := g.Verify(ctx, source)
	if err != nil {
		return nil, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return nil, err
	}
	manifest, err := g.Manifest(ctx, source.Path, target.HeadOID)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(manifest, target.Manifest) {
		return nil, failure("E_TARGET_MANIFEST_CHANGED", "target manifest differs from the pinned Git plan", "eve.toml")
	}
	m, err := config.Parse(manifest)
	if err != nil {
		return nil, err
	}
	tree, err := g.Tree(ctx, source.Path, target.HeadOID)
	if err != nil {
		return nil, err
	}
	bindings, err := bindings(m, tree)
	if err != nil {
		return nil, err
	}
	index, err := g.Index(ctx, source.Path)
	if err != nil {
		return nil, err
	}
	r, err := openRoot(source.Path, source.PathIdentity)
	if err != nil {
		return nil, err
	}
	defer r.fs.Close()
	selected := make(map[string][]string)
	for name, b := range bindings {
		selected[name] = slices.Clone(b.original)
	}
	warnings, err := selectCopies(ctx, r, m.Workspace.Copy, index, selected)
	if err != nil {
		return nil, err
	}
	if len(selected) > MaxFiles {
		return nil, failure("E_COPY_LIMIT", "selection exceeds 10000 files", "")
	}
	p := &Plan{source: source, target: target, manifest: m, inputs: make(map[string]input), report: Report{UnmatchedGlobs: warnings}}
	remaining := MaxCopyBytes
	names := slices.Sorted(maps.Keys(selected))
	for _, name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := selected[parent]; exists {
				return nil, failure("E_NATIVE_PATH_CONFLICT", "a selected file is also a destination parent", parent)
			}
		}
		entry := tree[name]
		b := bindings[name]
		file := File{Path: name, Native: b != nil, Tracked: entry.Mode != ""}
		if b != nil {
			file.Keys = slices.Sorted(maps.Keys(b.keys))
		}
		if entry.Mode != "" && entry.Mode != "100644" && entry.Mode != "100755" {
			return nil, failure("E_FILE_TYPE", "selected target must be a regular file", name)
		}
		if b != nil && file.Tracked {
			if b.credential {
				return nil, failure("E_TRACKED_CREDENTIAL_FILE", "provider credentials must never enter tracked files", name)
			}
			if !b.allowTracked {
				return nil, failure("E_TRACKED_FILE", "every native binding must authorize a tracked destination", name)
			}
		}
		for _, raw := range selected[name] {
			if _, _, err := treePath(tree, raw); err != nil {
				return nil, err
			}
			_, info, err := r.inspect(ctx, raw)
			if err != nil {
				return nil, err
			}
			if err := regular(info, false, name); err != nil {
				return nil, err
			}
		}
		item := input{file: file, original: selected[name], sourceTracked: index[name], binding: b}
		var base []byte
		if index[name] {
			if !file.Tracked {
				return nil, failure("E_COPY_TARGET_MISSING", "tracked source input has no tracked target counterpart", name)
			}
			item.file.Origin = "target" // Do not read dirty tracked source bytes.
		} else {
			limit := remaining
			if b != nil {
				limit = min(limit, envfile.MaxBytes)
			}
			item.local, err = r.read(ctx, name, limit, false)
			if err != nil {
				return nil, err
			}
			if item.local.info != nil {
				if file.Tracked {
					return nil, failure("E_COPY_CONFLICT", "untracked source input must not overwrite a tracked target", name)
				}
				item.file.Origin, base = "local", item.local.data
				remaining -= int64(len(base))
			} else if file.Tracked {
				item.file.Origin = "target"
			} else {
				item.file.Origin = "new"
			}
		}
		if b != nil {
			if item.file.Origin == "target" {
				base, err = g.Blob(ctx, source.Path, entry, min(remaining, envfile.MaxBytes))
				if err != nil {
					return nil, fileError(name, err)
				}
				remaining -= int64(len(base))
			}
			if err := validateDocument(base, b.keys); err != nil {
				return nil, fileError(name, err)
			}
		}
		p.inputs[name] = item
		p.report.Files = append(p.report.Files, item.file)
	}
	p.report.CopyBytes = MaxCopyBytes - remaining
	if err := p.verifySource(ctx, g, r); err != nil {
		return nil, err
	}
	return p, nil
}

func validateDocument(data []byte, keys map[string]bool) error {
	doc, err := envfile.Parse(data)
	if err != nil {
		return err
	}
	placeholders := make(map[string]string)
	for key := range keys {
		placeholders[key] = ""
	}
	_, err = doc.Apply(placeholders) // catches targeted duplicates, without evaluation
	return err
}

func (p *Plan) verifySource(ctx context.Context, g *git.Client, r *root) error {
	// Re-enumerate on a new validation pass; restored directory timestamps must
	// not make cached names authoritative for a new copy-selection snapshot.
	clear(r.dirs)
	r.entries = 0
	if _, err := g.Verify(ctx, p.source); err != nil {
		return err
	}
	index, err := g.Index(ctx, p.source.Path)
	if err != nil {
		return err
	}
	selected := make(map[string][]string)
	for name, item := range p.inputs {
		if item.binding != nil {
			selected[name] = slices.Clone(item.binding.original)
		}
	}
	if _, err := selectCopies(ctx, r, p.manifest.Workspace.Copy, index, selected); err != nil {
		return err
	}
	if len(selected) != len(p.inputs) {
		return failure("E_FILE_CHANGED", "copy selection changed; review a new snapshot", "")
	}
	for _, name := range slices.Sorted(maps.Keys(p.inputs)) {
		item := p.inputs[name]
		if _, ok := selected[name]; !ok || index[name] != item.sourceTracked {
			return failure("E_FILE_CHANGED", "copy selection or source tracking changed", name)
		}
		for _, raw := range item.original {
			if _, _, err := r.inspect(ctx, raw); err != nil {
				return err
			}
		}
		if !item.sourceTracked {
			if err := r.verify(ctx, name, item.local, false); err != nil {
				return err
			}
		}
	}
	return r.check()
}
