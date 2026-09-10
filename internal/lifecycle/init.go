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

type InitOptions struct {
	Project, CredentialProfile, BackendPath, SiteURLService string
	Convex, Update                                          bool
}
type InitBinding struct {
	Key, Output string
}
type InitService struct {
	ID, Path, Dev, ViteConfig, Port string
	PublicURL, PublicSiteURL        bool
	Bindings                        []InitBinding
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

// ProposeInit compiles reviewed backend, consumer and listener evidence into
// the existing v1 execution manifest. Backend discovery is independent from
// any supported launcher; unresolved loader evidence never creates a port claim.
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
	existing, err := loadExistingInitManifest(ctx, g, checkout, tree, options.Update)
	if err != nil {
		return InitResult{}, err
	}
	result := InitResult{Evidence: []string{"Committed filesystem/JSON/package evidence; package commands and dotenv values are not executed."}}
	services := exploreVitePackages(ctx, g, checkout, tree, &result)
	if len(services) == 0 {
		result.Warnings = append(result.Warnings, "No package with native PORT loader evidence was identified; backend-only configuration may still be valid.")
	}
	result.Services = services
	backends, err := findConvexBackends(ctx, g, checkout, tree)
	if err != nil {
		return result, err
	}
	backend := ""
	if options.BackendPath != "" {
		for _, candidate := range backends {
			if candidate == options.BackendPath {
				backend = candidate
				break
			}
		}
		if backend == "" {
			return result, proposalProblem("E_INIT_AMBIGUOUS", "explicit Convex backend path is not a single discovered candidate: "+strings.Join(backends, ", "))
		}
	} else {
		if len(backends) > 1 {
			return result, proposalProblem("E_INIT_AMBIGUOUS", "multiple Convex backend candidates require explicit selection: "+strings.Join(backends, ", "))
		}
		if len(backends) == 1 {
			backend = backends[0]
		}
	}
	if options.Convex && backend == "" {
		return result, proposalProblem("E_INIT_DISCOVERY", "no Convex backend was identified; pass an explicit backend path after committing its convex.json/package evidence")
	}
	result.HasConvexBackend = backend != ""
	if backend != "" && existing != nil && options.Project == "" {
		matched := 0
		for _, resource := range existing.Resources {
			if resource.Provider == "convex" && resource.Path == backend {
				matched++
				if resource.Project == "" {
					return result, proposalProblem("E_INIT_UPDATE", "existing backend requires its committed project binding before update")
				}
				options.Project = resource.Project
				if options.CredentialProfile == "" {
					options.CredentialProfile = resource.CredentialProfile
				}
			}
		}
		if matched > 1 {
			return result, proposalProblem("E_INIT_AMBIGUOUS", "multiple existing resources use the discovered backend path")
		}
	}
	if backend != "" {
		result.Evidence = append(result.Evidence, "Convex package "+backend+" has convex.json and a convex dependency in package.json")
		if err := validateInitProject(options.Project); err != nil {
			return result, err
		}
	} else if options.Project != "" {
		return result, proposalProblem("E_PROVIDER_INTENT", "--project was supplied but no exact Convex backend pattern was identified")
	}
	if len(services) == 0 && backend == "" {
		return result, proposalProblem("E_INIT_DISCOVERY", "no supported service or provider layout was identified; write eve.toml manually")
	}
	for i := range services {
		public, err := scanServicePublicKeys(ctx, g, checkout, tree, services[i].Path)
		if err != nil {
			return result, err
		}
		services[i].PublicURL = public["VITE_CONVEX_URL"]
		services[i].PublicSiteURL = public["VITE_CONVEX_SITE_URL"]
		if services[i].PublicURL {
			services[i].Bindings = append(services[i].Bindings, InitBinding{Key: "VITE_CONVEX_URL", Output: "url"})
		}
		if services[i].PublicSiteURL {
			services[i].Bindings = append(services[i].Bindings, InitBinding{Key: "VITE_CONVEX_SITE_URL", Output: "site_url"})
		}
	}
	if err := importEnvBindings(ctx, checkout, services, backend); err != nil {
		return result, err
	}
	merged, err := mergeInitManifest(existing, services, backend, options)
	if err != nil {
		return result, err
	}
	result.Manifest = renderInitManifest(merged)
	if backend == "" {
		result.Warnings = append(result.Warnings, "No exact Convex backend discovered; generated manifest is LOCAL-ONLY.")
	} else {
		if options.SiteURLService == "" {
			result.Warnings = append(result.Warnings, "No backend SITE_URL override is declared; Convex development defaults remain the source for shared remote configuration.")
		}
		result.Evidence = append(result.Evidence, "Convex development defaults are the zero-override backend configuration baseline; env list/update is skipped when no overrides are declared.")
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
		configName := prefixes[base]
		config, err := g.Blob(ctx, c.Identity.Path, tree[configName], 1<<20)
		if err != nil {
			result.Warnings = append(result.Warnings, "Vite config unreadable: "+base)
			continue
		}
		text := string(config)
		nativePort := strings.Contains(text, "loadEnv(") && strings.Contains(text, "strictPort") && strings.Contains(text, "PORT")
		port := ""
		if nativePort {
			port = "PORT"
		} else {
			result.Warnings = append(result.Warnings, "Vite config does not prove native strict PORT loading; no endpoint is assigned for "+base)
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
		services = append(services, InitService{ID: id, Path: base, Dev: "vite", ViteConfig: configName, Port: port})
		if port != "" {
			result.Evidence = append(result.Evidence, "service "+id+": exact vite leaf and native PORT evidence "+tree[configName].OID)
		} else {
			result.Evidence = append(result.Evidence, "service "+id+": exact vite leaf; listener support remains unresolved")
		}
	}
	return services
}
func vitePrefixes(tree map[string]git.TreeEntry) map[string]string {
	set := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(tree)) {
		entry := tree[name]
		if entry.Mode != "100644" {
			continue
		}
		base := path.Base(name)
		if base == "vite.config.js" || base == "vite.config.ts" {
			if _, exists := set[path.Dir(name)]; !exists {
				set[path.Dir(name)] = name
			}
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
func tomlString(value string) string { return fmt.Sprintf("%q", value) }
