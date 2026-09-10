package lifecycle

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"

	"eve/internal/config"
	"eve/internal/envfile"
	"eve/internal/files"
	"eve/internal/git"
)

func loadExistingInitManifest(ctx context.Context, g *git.Client, checkout git.Checkout, tree map[string]git.TreeEntry, update bool) (*config.Manifest, error) {
	_, exists := tree["eve.toml"]
	if !update {
		if exists {
			return nil, proposalProblem("E_INIT_EXISTS", "eve.toml already exists at the target revision")
		}
		return nil, nil
	}
	if !exists {
		return nil, proposalProblem("E_INIT_UPDATE", "--update requires a committed eve.toml")
	}
	if _, err := os.Stat(path.Join(checkout.Identity.Path, "eve.toml")); err != nil {
		return nil, proposalProblem("E_INIT_UPDATE", "committed eve.toml is not available in the working checkout")
	}
	data, err := g.Manifest(ctx, checkout.Identity.Path, checkout.HeadOID)
	if err != nil {
		return nil, err
	}
	return config.Parse(data)
}

var knownConvexBindings = map[string]string{
	"VITE_CONVEX_URL":             "url",
	"VITE_CONVEX_SITE_URL":        "site_url",
	"NEXT_PUBLIC_CONVEX_URL":      "url",
	"NEXT_PUBLIC_CONVEX_SITE_URL": "site_url",
	"CONVEX_URL":                  "url",
	"CONVEX_SITE_URL":             "site_url",
}

func importEnvBindings(ctx context.Context, checkout git.Checkout, services []InitService) error {
	for i := range services {
		name := baseEnvPath(services[i].Path)
		data, _, err := files.ReadDestination(ctx, checkout.Identity, name)
		if err != nil {
			return err
		}
		values, err := literalEnvValues(data)
		if err != nil {
			return proposalProblem("E_ENV_SYNTAX", name+" could not be audited without exposing values")
		}
		bound := map[string]string{}
		for _, binding := range services[i].Bindings {
			bound[binding.Key] = binding.Output
		}
		for _, key := range stringMapKeys(values) {
			output, found := knownConvexBindings[key]
			if !found {
				output = convexURLOutput(values[key])
				found = output != ""
			}
			if found {
				bound[key] = output
			}
		}
		services[i].Bindings = nil
		for _, key := range stringMapKeys(bound) {
			services[i].Bindings = append(services[i].Bindings, InitBinding{Key: key, Output: bound[key]})
		}
	}
	return nil
}

func stringMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func literalEnvValues(data []byte) (map[string]string, error) {
	if _, err := envfile.Parse(data); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		key, value, ok := literalEnvAssignment(line)
		if ok {
			values[key] = value
		}
	}
	return values, nil
}

func literalEnvAssignment(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export"))
	end := 0
	for end < len(line) && (unicode.IsLetter(rune(line[end])) || line[end] == '_' || end > 0 && (unicode.IsDigit(rune(line[end])) || line[end] == '.')) {
		end++
	}
	if end == 0 || !envfile.ValidKey(line[:end]) {
		return "", "", false
	}
	key = line[:end]
	rest := strings.TrimSpace(line[end:])
	if rest == "" || rest[0] != '=' {
		return "", "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if len(rest) >= 2 && (rest[0] == '"' || rest[0] == '\'') {
		quote := rest[0]
		closer := 1
		for closer < len(rest) && rest[closer] != quote {
			closer++
		}
		if closer >= len(rest) {
			return "", "", false
		}
		return key, rest[1:closer], true
	}
	if comment := strings.Index(rest, " #"); comment >= 0 {
		rest = rest[:comment]
	}
	return key, strings.TrimSpace(rest), true
}

func convexURLOutput(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.HasSuffix(host, ".convex.cloud") && len(host) > len(".convex.cloud") {
		return "url"
	}
	if strings.HasSuffix(host, ".convex.site") && len(host) > len(".convex.site") {
		return "site_url"
	}
	return ""
}

func mergeInitManifest(existing *config.Manifest, services []InitService, backend string, options InitOptions) (*config.Manifest, error) {
	out := &config.Manifest{Version: 1, Workspace: config.Workspace{PortBlockSize: 100}, Services: map[string]config.Service{}, Resources: map[string]config.Resource{}}
	if existing != nil {
		out.Workspace = existing.Workspace
		for id, service := range existing.Services {
			out.Services[id] = service
		}
		for id, resource := range existing.Resources {
			out.Resources[id] = resource
		}
	}
	if backend != "" {
		resource := config.Resource{Provider: "convex", Path: backend, Project: options.Project, EnvFile: ".env.local", CredentialProfile: recommendedProfile(options.CredentialProfile), TTL: "5d", Env: map[string]string{}}
		if options.SiteURLService != "" {
			resource.Env["SITE_URL"] = "${services." + options.SiteURLService + ".url}"
		}
		if current, exists := out.Resources["backend"]; exists {
			if current.Provider != resource.Provider || current.Path != resource.Path || current.Project != resource.Project {
				return nil, proposalProblem("E_INIT_UPDATE", "existing backend resource uses a different provider, path, project or resource identifier")
			}
			if current.Env == nil {
				current.Env = map[string]string{}
			}
			for key, value := range resource.Env {
				if _, exists := current.Env[key]; !exists {
					current.Env[key] = value
				}
			}
			out.Resources["backend"] = current
		} else {
			out.Resources["backend"] = resource
		}
	}
	for _, service := range services {
		bindings := map[string]string{}
		for _, binding := range service.Bindings {
			bindings[binding.Key] = "${resources.backend." + binding.Output + "}"
		}
		current, exists := out.Services[service.ID]
		if exists {
			if current.Path != service.Path {
				return nil, proposalProblem("E_INIT_UPDATE", "existing service has a different path for its identifier")
			}
			if current.Env == nil {
				current.Env = map[string]string{}
			}
			for key, value := range bindings {
				if _, exists := current.Env[key]; !exists {
					current.Env[key] = value
				}
			}
			out.Services[service.ID] = current
			continue
		}
		out.Services[service.ID] = config.Service{Path: service.Path, EnvFile: ".env.local", Port: service.Port, Host: "localhost", Scheme: "http", Env: bindings, Ports: map[string]config.ExtraPort{}}
	}
	siteSelected := false
	if service, exists := out.Services[options.SiteURLService]; exists && service.Port != "" {
		siteSelected = true
	}
	if options.SiteURLService != "" && (!siteSelected || out.Resources["backend"].Env["SITE_URL"] == "") {
	}
	return out, nil
}

func recommendedProfile(profile string) string {
	if profile == "" {
		return "default"
	}
	return profile
}

func renderInitManifest(manifest *config.Manifest) []byte {
	var out strings.Builder
	out.WriteString("# Generated by EVE after reviewed evidence. Values stay in project defaults.\nversion = 1\n")
	if manifest.Workspace.PortBlockSize != 100 || len(manifest.Workspace.Copy) != 0 {
		out.WriteString("\n[workspace]\n")
		if manifest.Workspace.PortBlockSize != 100 {
			out.WriteString("port_block_size = " + fmt.Sprint(manifest.Workspace.PortBlockSize) + "\n")
		}
		if len(manifest.Workspace.Copy) != 0 {
			values := make([]string, 0, len(manifest.Workspace.Copy))
			copyPatterns := slices.Clone(manifest.Workspace.Copy)
			slices.Sort(copyPatterns)
			for _, pattern := range copyPatterns {
				values = append(values, tomlString(pattern))
			}
			out.WriteString("copy = [" + strings.Join(values, ", ") + "]\n")
		}
	}
	for _, id := range stringMapKeys(manifest.Resources) {
		resource := manifest.Resources[id]
		out.WriteString("\n[resources." + id + "]\n")
		out.WriteString("provider = " + tomlString(resource.Provider) + "\npath = " + tomlString(resource.Path) + "\nproject = " + tomlString(resource.Project) + "\n")
		if resource.EnvFile != ".env.local" {
			out.WriteString("env_file = " + tomlString(resource.EnvFile) + "\n")
		}
		if resource.CredentialProfile != "default" {
			out.WriteString("credential_profile = " + tomlString(resource.CredentialProfile) + "\n")
		}
		if resource.TTL != "5d" {
			out.WriteString("ttl = " + tomlString(resource.TTL) + "\n")
		}
		if resource.Region != "" {
			out.WriteString("region = " + tomlString(resource.Region) + "\n")
		}
		if len(resource.Env) != 0 {
			out.WriteString("\n[resources." + id + ".env]\n")
			for _, key := range stringMapKeys(resource.Env) {
				out.WriteString(key + " = " + tomlString(resource.Env[key]) + "\n")
			}
		}
	}
	for _, id := range stringMapKeys(manifest.Services) {
		service := manifest.Services[id]
		out.WriteString("\n[services." + id + "]\n")
		out.WriteString("path = " + tomlString(service.Path) + "\nenv_file = " + tomlString(service.EnvFile) + "\n")
		if service.Port != "" {
			out.WriteString("port = " + tomlString(service.Port) + "\n")
		}
		if service.Host != "localhost" {
			out.WriteString("host = " + tomlString(service.Host) + "\n")
		}
		if service.Scheme != "http" {
			out.WriteString("scheme = " + tomlString(service.Scheme) + "\n")
		}
		if service.AllowTracked {
			out.WriteString("allow_tracked = true\n")
		}
		for _, name := range stringMapKeys(service.Ports) {
			port := service.Ports[name]
			out.WriteString("\n[services." + id + ".ports." + name + "]\nenv = " + tomlString(port.Env) + "\n")
		}
		if len(service.Env) != 0 {
			out.WriteString("\n[services." + id + ".env]\n")
			for _, key := range stringMapKeys(service.Env) {
				out.WriteString(key + " = " + tomlString(service.Env[key]) + "\n")
			}
		}
	}
	return []byte(out.String())
}
