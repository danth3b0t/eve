package files

import (
	"context"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"eve/internal/config"
	"eve/internal/git"
)

const MaxFiles = 10000
const MaxCopyBytes int64 = 256 << 20

type binding struct {
	keys                     map[string]bool
	original                 []string
	allowTracked, credential bool
}

func bindings(m *config.Manifest, tree map[string]git.TreeEntry) (map[string]*binding, error) {
	out := make(map[string]*binding)
	add := func(base, file string, allow, credential bool, keys []string) error {
		_, entry, err := treePath(tree, base)
		if err != nil {
			return err
		}
		if base != "." && entry.Mode != "040000" {
			return failure("E_FILE_TYPE", "service/resource path must be a target directory", base)
		}
		raw := base + "/" + file
		name, _, err := treePath(tree, raw)
		if err != nil {
			return err
		}
		b := out[name]
		if b == nil {
			b = &binding{keys: make(map[string]bool), allowTracked: true}
			out[name] = b
		}
		b.original = append(b.original, raw)
		b.allowTracked = b.allowTracked && allow
		b.credential = b.credential || credential
		for _, key := range keys {
			b.keys[key] = true
		}
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(m.Services)) {
		s := m.Services[id]
		keys := slices.Sorted(maps.Keys(s.Env))
		if s.Port != "" {
			keys = append(keys, s.Port)
		}
		for _, p := range s.Ports {
			keys = append(keys, p.Env)
		}
		if err := add(s.Path, s.EnvFile, s.AllowTracked, false, keys); err != nil {
			return nil, err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.Resources)) {
		r := m.Resources[id]
		if err := add(r.Path, r.EnvFile, false, true, []string{"CONVEX_DEPLOYMENT", "CONVEX_DEPLOY_KEY"}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// treePath checks original components against the pinned tree before cleaning.
// It cannot prove actual filesystem/ignore policy; Prepare checks those again.
func treePath(tree map[string]git.TreeEntry, raw string) (string, git.TreeEntry, error) {
	clean, err := config.RelativePath(".", raw)
	if err != nil {
		return "", git.TreeEntry{}, failure("E_PATH_ESCAPE", "invalid target path", raw)
	}
	current, missing := ".", false
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		if forbidden(part) {
			return "", git.TreeEntry{}, failure("E_PATH_ESCAPE", "protected path cannot be selected", raw)
		}
		if part == "." || part == "" {
			continue
		}
		if part == ".." {
			if missing {
				return "", git.TreeEntry{}, failure("E_PATH_ESCAPE", "cannot normalize through a missing target component", raw)
			}
			current = path.Dir(current)
			continue
		}
		current = path.Join(current, part)
		entry, ok := tree[current]
		if !ok {
			missing = true
			continue
		}
		if entry.Mode == "120000" {
			return "", git.TreeEntry{}, failure("E_SYMLINK", "target path traverses a committed symlink", current)
		}
		if i != len(parts)-1 && entry.Mode != "040000" {
			return "", git.TreeEntry{}, failure("E_FILE_TYPE", "target parent is not a directory", current)
		}
	}
	if clean == "." {
		return clean, git.TreeEntry{Mode: "040000", Size: -1}, nil
	}
	return clean, tree[clean], nil
}

func glob(pattern string) (*regexp.Regexp, string, error) {
	if _, err := config.RelativePath(".", pattern); err != nil {
		return nil, "", err
	}
	parts := strings.Split(pattern, "/")
	prefix := []string{}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, "", failure("E_COPY_PATTERN", "glob components must be normalized; no empty, . or .. components", pattern)
		}
		if forbidden(part) {
			return nil, "", failure("E_PATH_ESCAPE", "protected copy path", pattern)
		}
		if strings.ContainsAny(part, "*?") {
			break
		}
		prefix = append(prefix, part)
	}
	// Validate components after the first wildcard too.
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || forbidden(part) {
			return nil, "", failure("E_COPY_PATTERN", "unsafe glob component", pattern)
		}
	}
	var re strings.Builder
	re.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if (i == 0 || pattern[i-1] == '/') && i+2 < len(pattern) && pattern[i+2] == '/' {
					re.WriteString("(?:.*/)?")
					i += 2
				} else {
					re.WriteString(".*")
					i++
				}
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	re.WriteByte('$')
	compiled, err := regexp.Compile("(?s)" + re.String())
	if err != nil {
		return nil, "", failure("E_COPY_PATTERN", "invalid copy glob", pattern)
	}
	return compiled, path.Join(append([]string{"."}, prefix...)...), nil
}

func selectCopies(ctx context.Context, r *root, patterns []string, index map[string]bool, selected map[string][]string) ([]string, error) {
	var warnings []string
	visited := 0
	indexNames := slices.Sorted(maps.Keys(index))
	work := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > 10*maxEntries {
			return failure("E_FILE_LIMIT", "copy discovery work exceeds the bound; narrow the manifest", "")
		}
		return nil
	}
	add := func(name, raw string) error {
		if !slices.Contains(selected[name], raw) {
			selected[name] = append(selected[name], raw)
		}
		if len(selected) > MaxFiles {
			return failure("E_COPY_LIMIT", "copy selection exceeds 10000 files; narrow the manifest", "")
		}
		return nil
	}
	for _, pattern := range patterns {
		if !strings.ContainsAny(pattern, "*?") {
			name, info, err := r.inspect(ctx, pattern)
			if err != nil {
				return nil, err
			}
			if info == nil && !index[name] {
				return nil, failure("E_COPY_MISSING", "exact copy input is missing", name)
			}
			if info != nil {
				if err := regular(info, false, name); err != nil {
					return nil, err
				}
			}
			if err := add(name, pattern); err != nil {
				return nil, err
			}
			continue
		}
		re, prefix, err := glob(pattern)
		if err != nil {
			return nil, err
		}
		matched := false
		selectName := func(name string) error {
			if re.MatchString(name) {
				matched = true
				return add(name, name)
			}
			return nil
		}
		for _, name := range indexNames {
			if err := work(); err != nil {
				return nil, err
			}
			protected := false
			for _, part := range strings.Split(name, "/") {
				protected = protected || forbidden(part)
			}
			if !protected {
				if err := selectName(name); err != nil {
					return nil, err
				}
			}
		}
		_, info, err := r.inspect(ctx, prefix)
		if err != nil {
			return nil, err
		}
		var walk func(string) error
		walk = func(dir string) error {
			names, err := r.names(ctx, dir)
			if err != nil {
				return err
			}
			for _, name := range slices.Sorted(maps.Keys(names)) {
				if forbidden(name) {
					continue
				}
				if err := work(); err != nil {
					return err
				}
				name = path.Join(dir, name)
				info, err := r.fs.Lstat(name)
				if err != nil {
					return failure("E_FILE_CHANGED", "copy discovery entry disappeared or became inaccessible", name)
				}
				if info.IsDir() {
					if _, _, err := r.inspect(ctx, name); err != nil {
						return err
					}
					if err := walk(name); err != nil {
						return err
					}
				} else if info.Mode()&os.ModeSymlink != 0 && !re.MatchString(name) {
					// Never traverse a symlink looking for additional matches.
					continue
				} else if err := selectName(name); err != nil {
					return err
				}
			}
			return nil
		}
		if info != nil {
			if !info.IsDir() {
				return nil, failure("E_FILE_TYPE", "glob prefix must be a directory", prefix)
			}
			if err := walk(prefix); err != nil {
				return nil, err
			}
		}
		if !matched {
			warnings = append(warnings, pattern)
		}
	}
	return warnings, nil
}
