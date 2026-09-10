//go:build linux || darwin

package files

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"strings"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/platform"
	"eve/internal/private"
	"golang.org/x/sys/unix"
)

type PublicationFile struct {
	Image
	Temp      string
	Receipt   *domain.FileIdentity
	Published bool
}

func (f PublicationFile) String() string    { return f.Image.String() }
func (f PublicationFile) GoString() string  { return f.String() }
func TemporaryPath(name, ref string) string { return path.Join(path.Dir(name), ".eve-"+ref+".tmp") }

type Publisher struct {
	root         *root
	git          *git.Client
	id           domain.GitIdentity
	branch, head string
	files        []PublicationFile
	bindings     map[string]*binding
}

func (p Publisher) String() string   { return "files.Publisher" }
func (p Publisher) GoString() string { return p.String() }

// OpenPublisher is read-only. Its images must come from a complete durable
// Resource deployment bindings must already be present in those exact journaled
// images. OpenPublisher performs no provider calls and does not recover secrets.
func OpenPublisher(ctx context.Context, g *git.Client, id domain.GitIdentity, branch, head string, m *config.Manifest, images []PublicationFile) (*Publisher, error) {
	if m == nil || len(images) > MaxFiles {
		return nil, failure("E_PUBLICATION_PLAN", "a bounded publication batch is required", "")
	}
	tree, err := g.Tree(ctx, id.Path, head)
	if err != nil {
		return nil, err
	}
	bound, err := bindings(m, tree)
	if err != nil {
		return nil, err
	}
	r, err := openRoot(id.Path, id.PathIdentity)
	if err != nil {
		return nil, err
	}
	p := &Publisher{root: r, git: g, id: id, branch: branch, head: head, files: images, bindings: bound}
	seen := map[string]bool{}
	var total, pre int64
	for _, f := range images {
		name, err := config.RelativePath(".", f.Path)
		if err != nil || name == "." || name != f.Path || seen[name] || f.Mode != 0600 || path.Dir(f.Temp) != path.Dir(name) || !strings.HasPrefix(path.Base(f.Temp), ".eve-") || !strings.HasSuffix(f.Temp, ".tmp") || !private.ValidRef(strings.TrimSuffix(strings.TrimPrefix(path.Base(f.Temp), ".eve-"), ".tmp")) {
			r.fs.Close()
			return nil, failure("E_PUBLICATION_PLAN", "invalid destination or temporary path", name)
		}
		seen[name] = true
		total += int64(len(f.Data))
		pre += int64(len(f.Preimage))
		if total > MaxCopyBytes || pre > MaxCopyBytes {
			r.fs.Close()
			return nil, failure("E_COPY_LIMIT", "publication images exceed the byte budget", "")
		}
		if f.Tracked && (bound[name] == nil || !bound[name].allowTracked || bound[name].credential) {
			r.fs.Close()
			return nil, failure("E_TRACKED_FILE", "tracked publication requires unanimous manifest consent", name)
		}
	}
	for _, f := range images {
		if seen[f.Temp] {
			r.fs.Close()
			return nil, failure("E_PATH_ALIAS", "temporary path collides with a destination", f.Temp)
		}
	}
	if _, err := p.Check(ctx); err != nil {
		r.fs.Close()
		return nil, err
	}
	return p, nil
}
func (p *Publisher) Close() error { return p.root.fs.Close() }
func matches(s snapshot, id *domain.FileIdentity, data []byte) bool {
	if id == nil {
		return s.info == nil
	}
	return s.info != nil && *identity(s.info) == *id && bytes.Equal(s.data, data)
}
func (p *Publisher) observe(ctx context.Context, f PublicationFile) (bool, error) {
	s, err := p.root.read(ctx, f.Path, MaxCopyBytes, true)
	if err != nil {
		return false, err
	}
	post := f.Receipt != nil && matches(s, f.Receipt, f.Data) && s.info.Mode() == 0600
	if !post && (f.Published || !matches(s, f.PreimageIdentity, f.Preimage)) {
		return false, failure("E_FILE_CHANGED", "destination differs from its recorded preimage/postimage", f.Path)
	}
	temp, err := p.root.read(ctx, f.Temp, MaxCopyBytes, true)
	if err != nil {
		return false, err
	}
	if temp.info != nil {
		if post || f.Receipt == nil || !matches(temp, f.Receipt, f.Data) || temp.info.Mode() != 0600 {
			return false, failure("E_PUBLICATION_RECONCILE", "temporary is unreceipted, changed or unexpected; retain it for review", f.Temp)
		}
	} else if !post && f.Receipt != nil {
		return false, failure("E_PUBLICATION_RECONCILE", "receipted temporary is missing; do not recreate it", f.Temp)
	}
	return post, nil
}

// checkDestination repeats only the evidence tied to one mutation. Batch Git
// policy remains the OpenPublisher and final Check responsibility.
func (p *Publisher) checkDestination(ctx context.Context, f PublicationFile) (bool, error) {
	post, err := p.observe(ctx, f)
	if err != nil {
		return false, err
	}
	return post, p.root.check()
}

// Check rechecks the whole batch and actual Git policy, including after partial
// publication. Only receipted exact postimages and exact sibling temps explain
// visible worktree changes. Unrelated changes and all staged edits are refused.
func (p *Publisher) Check(ctx context.Context) (map[string]bool, error) {
	if err := p.git.CheckPublication(ctx, p.id, p.branch, p.head); err != nil {
		return nil, err
	}
	clear(p.root.dirs)
	p.root.entries = 0
	var names []string
	for _, f := range p.files {
		names = append(names, f.Path, f.Temp)
	}
	if err := checkAliases(ctx, p.root, names); err != nil {
		return nil, err
	}
	index, err := p.git.Index(ctx, p.id.Path)
	if err != nil {
		return nil, err
	}
	post := map[string]bool{}
	allowed := map[string]bool{}
	for _, f := range p.files {
		if index[f.Path] != f.Tracked || index[f.Temp] {
			return nil, failure("E_FILE_CHANGED", "destination/temporary tracking changed", f.Path)
		}
		if b := p.bindings[f.Path]; b != nil {
			for _, raw := range b.original {
				if _, _, err := p.root.inspect(ctx, raw); err != nil {
					return nil, err
				}
			}
		}
		if !f.Tracked {
			ignored, err := p.git.Ignored(ctx, p.id.Path, f.Path)
			if err != nil {
				return nil, err
			}
			if !ignored {
				return nil, ignoreError(f.Path)
			}
		}
		post[f.Path], err = p.observe(ctx, f)
		if err != nil {
			return nil, err
		}
		allowed[f.Path] = post[f.Path]
		allowed[f.Temp] = f.Receipt != nil && !post[f.Path]
	}
	changes, err := p.git.Changes(ctx, p.id.Path)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		permitted := allowed[c.Path] && ((c.Index == ' ' && c.Worktree == 'M') || (c.Index == '?' && c.Worktree == '?' && !index[c.Path]))
		if !permitted {
			return nil, failure("E_WORKTREE_DIRTY", "unreviewed or staged changes block initial publication", c.Path)
		}
	}
	return post, p.root.check()
}

func syncParent(parent *os.Root) error {
	f, err := parent.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return failure("E_FILE_IO", "cannot open parent for sync", "")
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return failure("E_FILE_IO", "cannot sync publication parent", "")
	}
	return nil
}
func (p *Publisher) parent(ctx context.Context, name string) (*os.Root, os.FileInfo, error) {
	parent := path.Dir(name)
	current := "."
	for _, part := range strings.Split(parent, "/") {
		if part == "." {
			continue
		}
		next := path.Join(current, part)
		_, info, err := p.root.inspect(ctx, next)
		if err != nil {
			return nil, nil, err
		}
		if info == nil {
			if err := p.root.fs.Mkdir(next, 0700); err != nil {
				return nil, nil, failure("E_FILE_CHANGED", "parent creation conflicted; review before retry", next)
			}
			ancestor, err := p.root.fs.OpenRoot(current)
			if err != nil {
				return nil, nil, failure("E_FILE_IO", "cannot sync new parent", next)
			}
			err = syncParent(ancestor)
			ancestor.Close()
			if err != nil {
				return nil, nil, err
			}
		} else if !info.IsDir() {
			return nil, nil, failure("E_FILE_TYPE", "publication parent is not a directory", next)
		}
		current = next
	}
	_, before, err := p.root.inspect(ctx, parent)
	if err != nil {
		return nil, nil, err
	}
	root, err := p.root.fs.OpenRoot(parent)
	if err != nil {
		return nil, nil, failure("E_FILE_IO", "cannot pin publication parent", parent)
	}
	after, err := root.Stat(".")
	if err != nil || before == nil || !os.SameFile(before, after) {
		root.Close()
		return nil, nil, failure("E_FILE_CHANGED", "publication parent changed", parent)
	}
	return root, before, nil
}

// Publish requires an already committed per-file intent. record persists the
// complete temporary identity before rename. Failures leave all evidence intact.
func (p *Publisher) Publish(ctx context.Context, name string, record func(domain.FileIdentity) error) error {
	if record == nil {
		return failure("E_PUBLICATION_PLAN", "a durable receipt callback is required", name)
	}
	i := -1
	for n := range p.files {
		if p.files[n].Path == name {
			i = n
			break
		}
	}
	if i < 0 {
		return failure("E_PUBLICATION_PLAN", "destination is not in the batch", name)
	}
	f := &p.files[i]
	post, err := p.checkDestination(ctx, *f)
	if err != nil {
		return err
	}
	parent, before, err := p.parent(ctx, name)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if post {
		return syncParent(parent)
	} // lost response: exact receipt verified
	if f.Receipt == nil {
		temp, err := parent.OpenFile(path.Base(f.Temp), os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
		if err != nil {
			return failure("E_PUBLICATION_RECONCILE", "temporary already exists or cannot be created safely", f.Temp)
		}
		defer temp.Close()
		info, err := temp.Stat()
		if err != nil || platform.ValidatePrivateInfo(info, false) != nil {
			return failure("E_FILE_TYPE", "temporary is not private and regular", f.Temp)
		}
		for data := f.Data; len(data) > 0; {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := temp.Write(data[:min(len(data), 64<<10)])
			if err != nil || n == 0 {
				return failure("E_FILE_IO", "cannot write publication temporary", f.Temp)
			}
			data = data[n:]
		}
		if err := temp.Sync(); err != nil {
			return failure("E_FILE_IO", "cannot sync publication temporary", f.Temp)
		}
		info, err = temp.Stat()
		if err != nil || platform.ValidatePrivateInfo(info, false) != nil {
			return failure("E_FILE_TYPE", "temporary changed while writing", f.Temp)
		}
		receipt := identity(info)
		if err := temp.Close(); err != nil {
			return failure("E_FILE_IO", "cannot close publication temporary", f.Temp)
		}
		if err := syncParent(parent); err != nil {
			return err
		}
		// Reread before recording; durable receipt must describe the full image.
		observed, err := p.root.read(ctx, f.Temp, MaxCopyBytes, true)
		if err != nil {
			return err
		}
		if !matches(observed, receipt, f.Data) {
			return failure("E_FILE_CHANGED", "temporary changed before receipt", f.Temp)
		}
		if err := record(*receipt); err != nil {
			return err
		}
		f.Receipt = receipt
	}
	postImage, err := p.checkDestination(ctx, *f)
	if err != nil {
		return err
	}
	if postImage {
		return failure("E_PUBLICATION_RECONCILE", "destination changed before its receipted rename", name)
	}
	_, after, err := p.root.inspect(ctx, path.Dir(name))
	if err != nil {
		return err
	}
	if after == nil || !os.SameFile(before, after) {
		return failure("E_FILE_CHANGED", "publication parent was replaced", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.PreimageIdentity == nil {
		err = renameExclusive(parent, path.Base(f.Temp), path.Base(name))
	} else {
		err = parent.Rename(path.Base(f.Temp), path.Base(name))
	}
	if err != nil {
		return failure("E_PUBLICATION_RECONCILE", "rename did not complete or its result is uncertain; retain the journal", name)
	}
	if err := syncParent(parent); err != nil {
		return err
	}
	postImage, err = p.checkDestination(ctx, *f)
	if err != nil {
		return err
	}
	if !postImage {
		return failure("E_PUBLICATION_RECONCILE", "published identity/content could not be verified", name)
	}
	f.Published = true
	return nil
}
