package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"eve/internal/domain"
	"eve/internal/git"
	"maps"
	"slices"
)

type InitOptions struct{ Project string }
type InitService struct {
	ID, Path, Dev, ViteConfig string
	PublicURL, PublicSiteURL  bool
}
type InitResult struct {
	Manifest         []byte `json:"-"`
	Evidence         []string
	Warnings         []string
	Services         []InitService
	HasConvexBackend bool
}

func proposalProblem(code, reason string) error { return &domain.Error{Code: code, Message: reason} }
func (r InitResult) ManifestText() string       { return string(r.Manifest) }

// ProposeInit restricts discovery to exact committed Bun/Vite and Convex
// fixture evidence. It never evaluates package code or arbitrary env files.
func ProposeInit(ctx context.Context, g *git.Client, root string, options InitOptions) (InitResult, error) {
	checkout, err := g.Inspect(ctx, root)
	if err != nil {
		return InitResult{}, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return InitResult{}, err
	}
	tree, err := g.Tree(ctx, checkout.Identity.Path, checkout.HeadOID)
	if err != nil {
		return InitResult{}, err
	}
	if _, hasManifest := tree["eve.toml"]; hasManifest {
		return InitResult{}, proposalProblem("E_INIT_EXISTS", "eve.toml already exists at the target revision")
	}
	result := InitResult{Evidence: []string{"Committed filesystem/JSON/package patterns only; commands are not evaluated."}}
	services := exploreVitePackages(ctx, g, checkout, tree, &result)
	if len(services) == 0 {
		result.Warnings = append(result.Warnings, "No strictly supported Bun/Vite package layout was identified.")
		return result, proposalProblem("E_INIT_DISCOVERY", "no exact supported service layout was identified; write eve.toml manually")
	}
	result.Services = services
	backends, err := findConvexBackends(ctx, g, checkout, tree)
	if err != nil {
		return result, err
	}
	if len(backends) > 1 {
		return result, proposalProblem("E_INIT_AMBIGUOUS", "multiple Convex backend candidates require explicit manual configuration: "+strings.Join(backends, ", "))
	}
	backend := ""
	if len(backends) == 1 {
		backend = backends[0]
	}
	result.HasConvexBackend = backend != ""
	if backend != "" {
		result.Evidence = append(result.Evidence, "Convex package "+backend+" has convex.json and a convex dependency in package.json")
		if err := validateInitProject(options.Project); err != nil {
			return result, err
		}
	} else if options.Project != "" {
		return result, proposalProblem("E_PROVIDER_INTENT", "--project was supplied but no exact Convex backend pattern was identified")
	}
	for i := range services {
		public, err := scanServicePublicKeys(ctx, g, checkout, tree, services[i].Path)
		if err != nil {
			return result, err
		}
		services[i].PublicURL = public["VITE_CONVEX_URL"]
		services[i].PublicSiteURL = public["VITE_CONVEX_SITE_URL"]
	}
	result.Manifest = renderInitManifest(services, backend, options.Project)
	if backend == "" {
		result.Warnings = append(result.Warnings, "No exact Convex backend discovered; generated manifest is LOCAL-ONLY.")
	} else {
		result.Warnings = append(result.Warnings, "SITE_URL is intentionally unresolved; map it to a reviewed service manually if the backend requires it.")
	}
	return result, nil
}
func exploreVitePackages(ctx context.Context, g *git.Client, c git.Checkout, tree map[string]git.TreeEntry, result *InitResult) []InitService {
	prefixes := vitePrefixes(tree)
	services := []InitService{}
	for _, base := range slices.Sorted(maps.Keys(prefixes)) {
		packageEntry, ok := tree[path.Join(base, "package.json")]
		if !ok {
			continue
		}
		packageData, err := g.Blob(ctx, c.Identity.Path, packageEntry, 1<<20)
		if err != nil {
			result.Warnings = append(result.Warnings, "package metadata unreadable at "+base)
			continue
		}
		var metadata struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(packageData, &metadata) != nil || metadata.Scripts["dev"] != "vite" {
			result.Warnings = append(result.Warnings, "not the proven exact `vite` leaf command: "+base)
			continue
		}
		configEntry := tree[path.Join(base, "vite.config.js")]
		config, err := g.Blob(ctx, c.Identity.Path, configEntry, 1<<20)
		if err != nil {
			result.Warnings = append(result.Warnings, "Vite config unreadable: "+base)
			continue
		}
		text := string(config)
		if !strings.Contains(text, "loadEnv(") || !strings.Contains(text, "strictPort") || !strings.Contains(text, "PORT") {
			result.Warnings = append(result.Warnings, "Vite config does not prove native PORT loading and strictPort: "+base)
			continue
		}
		ignored, err := g.Ignored(ctx, c.Identity.Path, baseEnvPath(base))
		if err != nil || !ignored {
			result.Warnings = append(result.Warnings, "review/ignore native destination before init: "+baseEnvPath(base))
			continue
		}
		id := serviceID(base)
		if id == "" {
			continue
		}
		services = append(services, InitService{ID: id, Path: base, Dev: "vite", ViteConfig: base + "/vite.config.js"})
		result.Evidence = append(result.Evidence, "service "+id+": exact vite leaf and native PORT evidence "+configEntry.OID)
	}
	return services
}
func vitePrefixes(tree map[string]git.TreeEntry) map[string]bool {
	set := map[string]bool{}
	for name, entry := range tree {
		if entry.Mode != "100644" {
			continue
		}
		if path.Base(name) == "vite.config.js" {
			set[path.Dir(name)] = true
		}
	}
	return set
}
func findConvexBackends(ctx context.Context, g *git.Client, checkout git.Checkout, tree map[string]git.TreeEntry) ([]string, error) {
	var candidates []string
	for _, name := range slices.Sorted(maps.Keys(tree)) {
		entry := tree[name]
		if entry.Mode != "100644" || path.Base(name) != "convex.json" || strings.Contains(name, "node_modules/") {
			continue
		}
		base := path.Dir(name)
		packageEntry, ok := tree[path.Join(base, "package.json")]
		if !ok {
			continue
		}
		data, err := g.Blob(ctx, checkout.Identity.Path, packageEntry, 1<<20)
		if err != nil {
			return nil, err
		}
		var metadata struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &metadata) != nil || (metadata.Dependencies["convex"] == "" && metadata.DevDependencies["convex"] == "") {
			continue
		}
		candidates = append(candidates, base)
	}
	return candidates, nil
}

func scanServicePublicKeys(ctx context.Context, g *git.Client, checkout git.Checkout, tree map[string]git.TreeEntry, base string) (map[string]bool, error) {
	found := map[string]bool{}
	prefix := ""
	if base != "." {
		prefix = base + "/"
	}
	source := map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".vue": true, ".svelte": true, ".html": true}
	for _, name := range slices.Sorted(maps.Keys(tree)) {
		if prefix != "" && !strings.HasPrefix(name, prefix) || strings.Contains(name, "/node_modules/") {
			continue
		}
		if !source[path.Ext(name)] || tree[name].Mode != "100644" {
			continue
		}
		data, err := g.Blob(ctx, checkout.Identity.Path, tree[name], 1<<20)
		if err != nil {
			return nil, err
		}
		text := string(data)
		found["VITE_CONVEX_URL"] = found["VITE_CONVEX_URL"] || strings.Contains(text, "VITE_CONVEX_URL")
		found["VITE_CONVEX_SITE_URL"] = found["VITE_CONVEX_SITE_URL"] || strings.Contains(text, "VITE_CONVEX_SITE_URL")
	}
	return found, nil
}
func serviceID(base string) string {
	if base == "." {
		return "web"
	}
	base = path.Base(base)
	var out []rune
	for i, ch := range base {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || (i > 0 && (ch == '-' || ch == '_')) {
			out = append(out, ch)
		}
	}
	if len(out) == 0 || out[0] < 'a' || out[0] > 'z' || len(out) > 48 {
		return ""
	}
	return string(out)
}
func baseEnvPath(base string) string {
	if base == "." {
		return ".env.local"
	}
	return base + "/.env.local"
}
func validateInitProject(project string) error {
	team, slug, ok := strings.Cut(project, ":")
	if !ok || !strings.ContainsFunc(team, func(ch rune) bool { return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' }) || !strings.ContainsFunc(slug, func(ch rune) bool { return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' }) {
		return proposalProblem("E_PROVIDER_IDENTITY", "explicit lower-case team:project binding required for discovered Convex backend")
	}
	return nil
}
func renderInitManifest(services []InitService, backend, project string) []byte {
	var out strings.Builder
	out.WriteString("# Review before use: listener/resource ownership is not inferred.\nversion = 1\n")
	if backend != "" {
		out.WriteString("\n[resources.backend]\nprovider = \"convex\"\npath = " + tomlString(backend) + "\nproject = " + tomlString(project) + "\n")
	}
	for _, service := range services {
		out.WriteString("\n[services." + service.ID + "]\npath = " + tomlString(service.Path) + "\nenv_file = \".env.local\"\nport = \"PORT\"\n")
		if backend != "" && (service.PublicURL || service.PublicSiteURL) {
			out.WriteString("\n[services." + service.ID + ".env]\n")
			if service.PublicURL {
				out.WriteString("VITE_CONVEX_URL = \"${resources.backend.url}\"\n")
			}
			if service.PublicSiteURL {
				out.WriteString("VITE_CONVEX_SITE_URL = \"${resources.backend.site_url}\"\n")
			}
		}
	}
	return []byte(out.String())
}
func tomlString(value string) string { return fmt.Sprintf("%q", value) }
