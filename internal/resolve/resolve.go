// Package resolve resolves public configuration after allocation/provisioning.
// It does not provision, read files, or supply provider selector/credential
// bindings. Those private bindings and filesystem identity checks are separate
// prerequisites for a complete publishable generation.
package resolve

import (
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"

	"eve/internal/config"
	"eve/internal/envfile"
	"eve/internal/interpolate"
)

type Endpoint struct{ Service, Name string }
type Workspace struct {
	ID, Slug, Branch string
	PortBase         *int
}

// No credential field: secrets cannot become interpolation outputs.
type ResourceOutputs struct{ URL, SiteURL, Deployment, Name, Reference string }
type Inputs struct {
	Workspace Workspace
	Ports     map[Endpoint]int
	Resources map[string]ResourceOutputs
}
type File struct {
	Path         string
	AllowTracked bool
	Values       map[string]string `json:"-"`
	Owners       map[string][]string
}
type Result struct {
	Files     []File
	RemoteEnv map[string]map[string]string `json:"-"`
}
type Error struct {
	Code, Path, Key, Reason string
	Owners                  []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s path=%q key=%q owners=%q: %s", e.Code, e.Path, e.Key, e.Owners, e.Reason)
}

// Resolve expects a manifest returned by config.Parse. File paths are lexically
// normalized, not filesystem-canonical: case/link/identity checks are still due.
func Resolve(m *config.Manifest, in Inputs) (*Result, error) {
	if m == nil {
		return nil, &Error{Code: "E_MANIFEST_INVALID", Reason: "manifest required"}
	}
	values := map[string]string{}
	for key, value := range map[string]string{"workspace.id": in.Workspace.ID, "workspace.slug": in.Workspace.Slug, "workspace.branch": in.Workspace.Branch} {
		if value != "" {
			values[key] = value
		}
	}
	endpoints := m.Endpoints()
	badPort := func() error {
		return &Error{Code: "E_PORT_ALLOCATION", Reason: "missing, duplicate, out-of-range or out-of-block endpoint allocation"}
	}
	if len(endpoints) == 0 {
		if in.Workspace.PortBase != nil {
			return nil, badPort()
		}
	} else {
		if in.Workspace.PortBase == nil {
			return nil, badPort()
		}
		base := *in.Workspace.PortBase
		if base < 1 || base > 65535 || m.Workspace.PortBlockSize < 1 || m.Workspace.PortBlockSize > 65536-base {
			return nil, badPort()
		}
		values["workspace.port_base"] = strconv.Itoa(base)
		seen := make(map[int]bool)
		for _, ep := range endpoints {
			port, ok := in.Ports[Endpoint{ep.Service, ep.Name}]
			if !ok || port < base || port >= base+m.Workspace.PortBlockSize || seen[port] {
				return nil, badPort()
			}
			seen[port] = true
			prefix := "services." + ep.Service
			if ep.Name == "primary" {
				values[prefix+".port"] = strconv.Itoa(port)
				s := m.Services[ep.Service]
				values[prefix+".url"] = s.Scheme + "://" + net.JoinHostPort(s.Host, strconv.Itoa(port))
			} else {
				values[prefix+".ports."+ep.Name+".port"] = strconv.Itoa(port)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.Resources)) {
		r, ok := in.Resources[id]
		if !ok {
			return nil, &Error{Code: "E_RESOURCE_OUTPUT", Reason: "declared resource has no verified public outputs", Owners: []string{"resources." + id}}
		}
		for suffix, value := range map[string]string{"url": r.URL, "site_url": r.SiteURL, "deployment": r.Deployment, "name": r.Name, "reference": r.Reference} {
			if value != "" {
				values["resources."+id+"."+suffix] = value
			}
		}
	}
	files := make(map[string]*File)
	type destinationKey struct{ path, key string }
	conflicts := make(map[destinationKey]bool)
	for _, id := range slices.Sorted(maps.Keys(m.Services)) {
		s := m.Services[id]
		dest, err := config.RelativePath(s.Path, s.EnvFile)
		if err != nil {
			return nil, err
		}
		file := files[dest]
		if file == nil {
			file = &File{Path: dest, AllowTracked: s.AllowTracked, Values: make(map[string]string), Owners: make(map[string][]string)}
			files[dest] = file
		} else {
			file.AllowTracked = file.AllowTracked && s.AllowTracked
		}
		prefix := "services." + id
		merge := func(key, value, owner string) error {
			if !envfile.ValidKey(key) || config.ReservedLocalKey(key) {
				return &Error{Code: "E_ENV_KEY", Path: dest, Key: key, Reason: "invalid or provider-reserved key"}
			}
			if err := envfile.ValidateValue(value); err != nil {
				return &Error{Code: "E_ENV_SERIALIZATION", Path: dest, Key: key, Reason: "generated value is outside the portable dotenv subset"}
			}
			if previous, exists := file.Values[key]; exists && previous != value {
				conflicts[destinationKey{dest, key}] = true
			}
			file.Values[key] = value
			file.Owners[key] = append(file.Owners[key], owner)
			return nil
		}
		if s.Port != "" {
			if err := merge(s.Port, values[prefix+".port"], prefix+".port"); err != nil {
				return nil, err
			}
		}
		for _, name := range slices.Sorted(maps.Keys(s.Ports)) {
			if err := merge(s.Ports[name].Env, values[prefix+".ports."+name+".port"], prefix+".ports."+name); err != nil {
				return nil, err
			}
		}
		for _, key := range slices.Sorted(maps.Keys(s.Env)) {
			value, err := evaluate(s.Env[key], values, dest, key)
			if err != nil {
				return nil, err
			}
			if err := merge(key, value, prefix+".env."+key); err != nil {
				return nil, err
			}
		}
	}
	for _, dest := range slices.Sorted(maps.Keys(files)) {
		for _, key := range slices.Sorted(maps.Keys(files[dest].Owners)) {
			if conflicts[destinationKey{dest, key}] {
				return nil, &Error{Code: "E_NATIVE_ENV_CONFLICT", Path: dest, Key: key, Owners: slices.Clone(files[dest].Owners[key]), Reason: "owners require different values"}
			}
		}
	}
	result := &Result{RemoteEnv: make(map[string]map[string]string)}
	for _, id := range slices.Sorted(maps.Keys(m.Resources)) {
		result.RemoteEnv[id] = make(map[string]string)
		for _, key := range slices.Sorted(maps.Keys(m.Resources[id].Env)) {
			value, err := evaluate(m.Resources[id].Env[key], values, "resources."+id+".env", key)
			if err != nil {
				return nil, err
			}
			// Remote application values are JSON strings, not local dotenv.
			result.RemoteEnv[id][key] = value
		}
	}
	for _, dest := range slices.Sorted(maps.Keys(files)) {
		result.Files = append(result.Files, *files[dest])
	}
	return result, nil
}

func evaluate(input string, values map[string]string, path, key string) (string, error) {
	expr, err := interpolate.Parse(input)
	if err == nil {
		var value string
		value, err = expr.Evaluate(values)
		if err == nil {
			return value, nil
		}
	}
	return "", &Error{Code: "E_REFERENCE", Path: path, Key: key, Reason: "invalid or unresolved reference"}
}
