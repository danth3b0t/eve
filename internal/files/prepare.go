package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/git"
	"eve/internal/resolve"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Image is private input for a future journaled publisher, not authorization to
// write. Never use an unjournaled WriteFile loop to materialize these images.
type Image struct {
	Path             string
	Mode             os.FileMode
	Tracked          bool
	PreimageIdentity *domain.FileIdentity
	Data, Preimage   []byte            `json:"-"`
	Values           map[string]string `json:"-"`
}

func (i Image) String() string   { data, _ := json.Marshal(i); return string(data) }
func (i Image) GoString() string { return i.String() }

// Prepare validates a linked checkout and constructs local configuration images.
// Provider secrets must come through a verified provider lifecycle checkpoint.
func (p *Plan) Prepare(ctx context.Context, g *git.Client, target domain.GitIdentity, in resolve.Inputs) ([]Image, error) {
	return p.PrepareWithBindings(ctx, g, target, in, nil)
}

// PrepareWithBindings overlays only CONVEX_DEPLOYMENT/CONVEX_DEPLOY_KEY in a
// declared resource destination. Values stay Excluded from JSON/reports.
func (p *Plan) PrepareWithBindings(ctx context.Context, g *git.Client, target domain.GitIdentity, in resolve.Inputs, secrets map[string]map[string]string) ([]Image, error) {
	checkout, err := g.Verify(ctx, target)
	if err != nil {
		return nil, err
	}
	if target.Path == p.source.Path || target.CommonIdentity != p.source.CommonIdentity || target.CommonDir != p.source.CommonDir || target.AdminDir == target.CommonDir {
		return nil, failure("E_GIT_IDENTITY", "target must be a distinct linked checkout in the planned repository", target.Path)
	}
	if checkout.HeadOID != p.target.HeadOID || checkout.Branch != p.target.Branch {
		return nil, failure("E_GIT_REF_CHANGED", "target checkout differs from the frozen file plan", target.Path)
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return nil, err
	}
	source, err := openRoot(p.source.Path, p.source.PathIdentity)
	if err != nil {
		return nil, err
	}
	defer source.fs.Close()
	if err := p.verifySource(ctx, g, source); err != nil {
		return nil, err
	}
	dest, err := openRoot(target.Path, target.PathIdentity)
	if err != nil {
		return nil, err
	}
	defer dest.fs.Close()
	preimages, err := p.checkTarget(ctx, g, dest)
	if err != nil {
		return nil, err
	}
	if err := p.validateBindings(secrets); err != nil {
		return nil, err
	}
	resolved, err := resolve.Resolve(p.manifest, in)
	if err != nil {
		return nil, err
	}
	values := make(map[string]map[string]string)
	for _, file := range resolved.Files {
		values[file.Path] = file.Values
	}
	for name, overrides := range secrets {
		merged := maps.Clone(values[name])
		if merged == nil {
			merged = make(map[string]string)
		}
		for key, value := range overrides {
			merged[key] = value
		}
		values[name] = merged
	}
	var images []Image
	remaining := MaxCopyBytes
	for _, file := range p.report.Files {
		item := p.inputs[file.Path]
		if !file.Native && file.Origin == "target" {
			continue
		} // Git already supplied it; no write.
		base := item.local.data
		if file.Origin == "target" {
			base = preimages[file.Path].data
		}
		data := bytes.Clone(base)
		if file.Native {
			doc, err := envfile.Parse(base)
			if err != nil {
				return nil, fileError(file.Path, err)
			}
			data, err = doc.Apply(values[file.Path])
			if err != nil {
				return nil, fileError(file.Path, err)
			}
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, failure("E_COPY_LIMIT", "final images exceed 256 MiB; narrow the manifest", "")
		}
		images = append(images, Image{Path: file.Path, Mode: 0600, Tracked: file.Tracked, PreimageIdentity: identity(preimages[file.Path].info), Data: data, Preimage: bytes.Clone(preimages[file.Path].data), Values: maps.Clone(values[file.Path])})
	}
	// Recheck content as well as metadata: restored timestamps are not proof of
	// unchanged inputs. No SQL or application write occurs between these checks.
	for _, file := range p.report.Files {
		if file.Native || !file.Tracked {
			if err := dest.verify(ctx, file.Path, preimages[file.Path], true); err != nil {
				return nil, err
			}
		}
	}
	if err := p.verifySource(ctx, g, source); err != nil {
		return nil, err
	}
	latest, err := p.checkTarget(ctx, g, dest)
	if err != nil {
		return nil, err
	}
	for name, before := range preimages {
		after := latest[name]
		if before.info == nil && after.info == nil {
			continue
		}
		if !same(before.info, after.info) || !bytes.Equal(before.data, after.data) {
			return nil, failure("E_FILE_CHANGED", "target changed while preparing images", name)
		}
	}
	checkout, err = g.Verify(ctx, target)
	if err != nil {
		return nil, err
	}
	if checkout.HeadOID != p.target.HeadOID || checkout.Branch != p.target.Branch {
		return nil, failure("E_GIT_REF_CHANGED", "target revision changed while preparing images", target.Path)
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return nil, err
	}
	if err := dest.check(); err != nil {
		return nil, err
	}
	return images, nil
}

func (p *Plan) validateBindings(secrets map[string]map[string]string) error {
	if len(p.manifest.Resources) == 0 && len(secrets) != 0 {
		return failure("E_PROVIDER_INTENT", "resource bindings do not belong to this manifest", "")
	}
	if len(secrets) != len(p.manifest.Resources) {
		return failure("E_PROVIDER_BINDING_PENDING", "verified deployment credentials are required for every resource", "")
	}
	expected := map[string]bool{}
	for _, resource := range p.manifest.Resources {
		name, err := config.RelativePath(resource.Path, resource.EnvFile)
		if err != nil {
			return err
		}
		if expected[name] {
			return failure("E_NATIVE_ENV_CONFLICT", "separate resources would need distinct native selectors in one file", name)
		}
		expected[name] = true
	}
	for name, values := range secrets {
		item, ok := p.inputs[name]
		if !ok || !expected[name] || item.binding == nil || !item.binding.credential {
			return failure("E_PROVIDER_INTENT", "binding destination is not a credential-only manifest declaration", name)
		}
		if len(values) != 2 || values["CONVEX_DEPLOYMENT"] == "" || values["CONVEX_DEPLOY_KEY"] == "" {
			return failure("E_PROVIDER_INTENT", "exact native Convex selector and deployment key are required", name)
		}
		for key, value := range values {
			if !config.ReservedLocalKey(key) || envfile.ValidateValue(value) != nil {
				return failure("E_ENV_SERIALIZATION", "native binding cannot be represented safely", name)
			}
		}
	}
	return nil
}

func (p *Plan) checkTarget(ctx context.Context, g *git.Client, dest *root) (map[string]snapshot, error) {
	clear(dest.dirs)
	dest.entries = 0
	index, err := g.Index(ctx, dest.path)
	if err != nil {
		return nil, err
	}
	changes, err := g.Changes(ctx, dest.path)
	if err != nil {
		return nil, err
	}
	dirty := make(map[string]bool)
	for _, change := range changes {
		dirty[change.Path] = true
	}
	preimages := make(map[string]snapshot)
	remaining := MaxCopyBytes
	names := slices.Sorted(maps.Keys(p.inputs))
	if err := checkAliases(ctx, dest, names); err != nil {
		return nil, err
	}
	for _, name := range names {
		item := p.inputs[name]
		if item.binding != nil && item.binding.credential && index[name] {
			return nil, failure("E_TRACKED_CREDENTIAL_FILE", "provider credentials must never enter tracked files", name)
		}
		if index[name] != item.file.Tracked {
			return nil, failure("E_FILE_CHANGED", "target tracking changed since planning", name)
		}
		for _, raw := range item.original {
			_, info, err := dest.inspect(ctx, raw)
			if err != nil {
				return nil, err
			}
			if err := regular(info, true, name); err != nil {
				return nil, err
			}
			if !item.file.Tracked && info != nil {
				return nil, failure("E_FILE_CHANGED", "untracked destination already exists; do not overwrite or adopt it", name)
			}
			if item.file.Tracked && info == nil {
				return nil, failure("E_FILE_CHANGED", "tracked target input is missing", name)
			}
		}
		if item.file.Tracked && dirty[name] {
			return nil, failure("E_FILE_CHANGED", "tracked target input was edited after checkout", name)
		}
		if !item.file.Tracked {
			ignored, err := g.Ignored(ctx, dest.path, name)
			if err != nil {
				return nil, err
			}
			if !ignored {
				return nil, ignoreError(name)
			}
		}
		if item.file.Native {
			preimages[name], err = dest.read(ctx, name, min(remaining, envfile.MaxBytes), true)
			if err != nil {
				return nil, err
			}
			remaining -= int64(len(preimages[name].data))
		}
	}
	return preimages, dest.check()
}

func ignoreError(name string) error {
	if strings.ContainsAny(name, "\r\n") {
		return failure("E_IGNORE_MISSING", "path needs a reviewed ignore rule but cannot be represented by a single literal ignore entry", name)
	}
	var entry strings.Builder
	entry.WriteByte('/')
	for _, ch := range name {
		if strings.ContainsRune("\\*?[] ", ch) {
			entry.WriteByte('\\')
		}
		entry.WriteRune(ch)
	}
	return failure("E_IGNORE_MISSING", fmt.Sprintf("add the reviewed root .gitignore entry %q; EVE will not modify ignore rules", entry.String()), name)
}

// Existing distinct case-sensitive names are supported. When destination
// components do not exist yet, ambiguous case/normalization variants are refused
// conservatively, rather than mutating a repository to probe its case behavior.
func checkAliases(ctx context.Context, r *root, names []string) error {
	seen := make(map[string]string)
	fold := cases.Fold()
	for _, name := range names {
		for current := name; current != "."; current = path.Dir(current) {
			key := norm.NFC.String(fold.String(current))
			if previous, ok := seen[key]; ok && previous != current {
				_, a, err := r.inspect(ctx, previous)
				if err != nil {
					return err
				}
				_, b, err := r.inspect(ctx, current)
				if err != nil {
					return err
				}
				if a == nil || b == nil || os.SameFile(a, b) {
					return failure("E_PATH_ALIAS", "ambiguous destination spellings; use unambiguous paths", current)
				}
			}
			seen[key] = current
		}
	}
	return nil
}
