// Package config loads data-only manifests. Validation here is static: callers
// must still verify Git identity, filesystem containment/links, tracking/ignore
// rules, provider identity, and native configuration consumption before mutation.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"eve/internal/envfile"
	"eve/internal/interpolate"
	"github.com/pelletier/go-toml/v2"
)

const MaxBytes = 1 << 20

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var projectBinding = regexp.MustCompile(`^[^\s/:]+:[^\s/:]+$`)
var duration = regexp.MustCompile(`^[1-9][0-9]*[mhd]$`)

type Error struct {
	Code, Field, Reason string
	Line, Column        int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s field=%q line=%d column=%d: %s", e.Code, e.Field, e.Line, e.Column, e.Reason)
}
func invalid(field, reason string) error {
	return &Error{Code: "E_MANIFEST_INVALID", Field: field, Reason: reason}
}

type Manifest struct {
	Version   int
	Workspace Workspace
	Services  map[string]Service
	Resources map[string]Resource
}
type Workspace struct {
	Copy          []string
	PortBlockSize int
}
type Service struct {
	Path, EnvFile, Port, Host, Scheme string
	AllowTracked                      bool
	Env                               map[string]string
	Ports                             map[string]ExtraPort
}
type ExtraPort struct {
	Env string `toml:"env"`
}
type Resource struct {
	Provider, Path, Project, EnvFile, CredentialProfile, TTL, Region string
	Env                                                              map[string]string
}

// Wire optionals distinguish omission from an explicitly invalid zero/empty
// value. Defaults are applied only after strict decoding, never by truthiness.
type manifestWire struct {
	Version   int `toml:"version"`
	Workspace struct {
		Copy          []string `toml:"copy"`
		PortBlockSize *int     `toml:"port_block_size"`
	} `toml:"workspace"`
	Services  map[string]serviceWire  `toml:"services"`
	Resources map[string]resourceWire `toml:"resources"`
}
type serviceWire struct {
	Path         string               `toml:"path"`
	EnvFile      string               `toml:"env_file"`
	Port         *string              `toml:"port"`
	Host         *string              `toml:"host"`
	Scheme       *string              `toml:"scheme"`
	AllowTracked bool                 `toml:"allow_tracked"`
	Env          map[string]string    `toml:"env"`
	Ports        map[string]ExtraPort `toml:"ports"`
}
type resourceWire struct {
	Provider          string            `toml:"provider"`
	Path              string            `toml:"path"`
	Project           string            `toml:"project"`
	EnvFile           *string           `toml:"env_file"`
	CredentialProfile *string           `toml:"credential_profile"`
	TTL               *string           `toml:"ttl"`
	Region            *string           `toml:"region"`
	Env               map[string]string `toml:"env"`
}

func decode(data []byte, out any) error {
	if len(data) > MaxBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return invalid("", "input exceeds 1 MiB or is invalid text")
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(out); err != nil {
		e := &Error{Code: "E_MANIFEST_INVALID", Reason: "invalid TOML syntax or field type"}
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			e.Reason = "unknown field"
		}
		var position *toml.DecodeError
		if errors.As(err, &position) {
			e.Line, e.Column = position.Position()
		}
		// The TOML decoder's Error/String methods may include a secret-bearing
		// value or source excerpt. Keep only safe position metadata.
		return e
	}
	// go-toml's struct matching is case-insensitive even in strict mode.
	// Check exact TOML tag names too; dynamic service/env map keys stay intact.
	var table map[string]any
	if err := toml.Unmarshal(data, &table); err != nil {
		return invalid("", "invalid TOML")
	}
	return exactFields(table, reflect.TypeOf(out).Elem(), "")
}

func exactFields(value any, typ reflect.Type, field string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	table, ok := value.(map[string]any)
	if !ok {
		return nil
	} // Field types were checked by the strict decoder.
	switch typ.Kind() {
	case reflect.Struct:
		fields := make(map[string]reflect.Type, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			fields[f.Tag.Get("toml")] = f.Type
		}
		for _, key := range slices.Sorted(maps.Keys(table)) {
			child := key
			if field != "" {
				child = field + "." + key
			}
			ft, ok := fields[key]
			if !ok {
				return invalid(child, "unknown field (names are case-sensitive)")
			}
			if err := exactFields(table[key], ft, child); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range slices.Sorted(maps.Keys(table)) {
			if err := exactFields(table[key], typ.Elem(), field+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func withDefault[T any](value *T, fallback T) T {
	if value != nil {
		return *value
	}
	return fallback
}

func Parse(data []byte) (*Manifest, error) {
	var raw manifestWire
	if err := decode(data, &raw); err != nil {
		return nil, err
	}
	m := &Manifest{
		Version:   raw.Version,
		Workspace: Workspace{Copy: raw.Workspace.Copy, PortBlockSize: withDefault(raw.Workspace.PortBlockSize, 100)},
		Services:  make(map[string]Service, len(raw.Services)), Resources: make(map[string]Resource, len(raw.Resources)),
	}
	if m.Version != 1 {
		return nil, invalid("version", "required version is 1")
	}
	if len(raw.Services)+len(raw.Resources) == 0 {
		return nil, invalid("", "at least one service or resource is required")
	}
	if m.Workspace.PortBlockSize < 1 || m.Workspace.PortBlockSize > 1000 {
		return nil, invalid("workspace.port_block_size", "must be between 1 and 1000")
	}
	seen := make(map[string]bool)
	for _, pattern := range m.Workspace.Copy {
		if seen[pattern] {
			return nil, invalid("workspace.copy", "duplicate copy pattern")
		}
		seen[pattern] = true
		if _, err := RelativePath(".", pattern); err != nil {
			return nil, invalid("workspace.copy", "unsafe or empty copy pattern")
		}
		if strings.ContainsAny(pattern, "{}[]\\") || strings.HasPrefix(pattern, "!") {
			return nil, invalid("workspace.copy", "only literal paths and *, ?, ** globs are supported")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(raw.Services)) {
		w := raw.Services[id]
		field := "services." + id
		if !identifier.MatchString(id) {
			return nil, invalid("services", "invalid service identifier")
		}
		s := Service{Path: w.Path, EnvFile: w.EnvFile, Port: withDefault(w.Port, ""), Host: withDefault(w.Host, "localhost"),
			Scheme: withDefault(w.Scheme, "http"), AllowTracked: w.AllowTracked, Env: w.Env, Ports: w.Ports}
		if _, err := RelativePath(".", s.Path); err != nil {
			return nil, invalid(field+".path", "unsafe or empty service path")
		}
		if _, err := RelativePath(s.Path, s.EnvFile); err != nil {
			return nil, invalid(field+".env_file", "unsafe or empty destination")
		}
		if w.Port != nil && !envfile.ValidKey(s.Port) {
			return nil, invalid(field+".port", "invalid environment key")
		}
		if ReservedLocalKey(s.Port) {
			return nil, invalid(field+".port", "provider-reserved key")
		}
		if s.Host != "localhost" && s.Host != "127.0.0.1" && s.Host != "::1" {
			return nil, invalid(field+".host", "host must be loopback")
		}
		if s.Scheme != "http" && s.Scheme != "https" {
			return nil, invalid(field+".scheme", "scheme must be http or https")
		}
		for _, name := range slices.Sorted(maps.Keys(s.Ports)) {
			if name == "primary" || !identifier.MatchString(name) {
				return nil, invalid(field+".ports", "invalid or reserved endpoint name")
			}
			if !envfile.ValidKey(s.Ports[name].Env) || ReservedLocalKey(s.Ports[name].Env) {
				return nil, invalid(field+".ports."+name+".env", "invalid or provider-reserved key")
			}
		}
		if err := validateEnv(field+".env", s.Env, false); err != nil {
			return nil, err
		}
		m.Services[id] = s
	}
	for _, id := range slices.Sorted(maps.Keys(raw.Resources)) {
		w := raw.Resources[id]
		field := "resources." + id
		if !identifier.MatchString(id) {
			return nil, invalid("resources", "invalid resource identifier")
		}
		r := Resource{Provider: w.Provider, Path: w.Path, Project: w.Project, EnvFile: withDefault(w.EnvFile, ".env.local"),
			CredentialProfile: withDefault(w.CredentialProfile, "default"), TTL: withDefault(w.TTL, "5d"), Region: withDefault(w.Region, ""), Env: w.Env}
		if r.Provider != "convex" {
			return nil, invalid(field+".provider", "only convex is supported")
		}
		if _, err := RelativePath(".", r.Path); err != nil {
			return nil, invalid(field+".path", "unsafe or empty resource path")
		}
		if _, err := RelativePath(r.Path, r.EnvFile); err != nil {
			return nil, invalid(field+".env_file", "unsafe or empty destination")
		}
		if !projectBinding.MatchString(r.Project) {
			return nil, invalid(field+".project", "explicit team:project binding required")
		}
		if !identifier.MatchString(r.CredentialProfile) {
			return nil, invalid(field+".credential_profile", "invalid credential profile name")
		}
		if _, err := ParseTTL(r.TTL); err != nil {
			return nil, invalid(field+".ttl", "positive m/h/d duration required, within representable limits")
		}
		if w.Region != nil && r.Region == "" {
			return nil, invalid(field+".region", "region must not be empty")
		}
		if err := validateEnv(field+".env", r.Env, true); err != nil {
			return nil, err
		}
		m.Resources[id] = r
	}
	endpointCount := len(m.Endpoints())
	if endpointCount > m.Workspace.PortBlockSize {
		return nil, invalid("workspace.port_block_size", "declared endpoints exceed block capacity")
	}
	for _, id := range slices.Sorted(maps.Keys(m.Services)) {
		if err := m.validateReferences("services."+id+".env", m.Services[id].Env, endpointCount != 0); err != nil {
			return nil, err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.Resources)) {
		if err := m.validateReferences("resources."+id+".env", m.Resources[id].Env, endpointCount != 0); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// RelativePath performs lexical containment only, never symlink resolution. A
// successful result is NOT permission to access that path. Filesystem checks
// must examine source/target paths again, including their original components.
func RelativePath(base, relative string) (string, error) {
	for _, p := range []string{base, relative} {
		if p == "" || path.IsAbs(p) || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
			return "", invalid("path", "path must be relative, nonempty valid text")
		}
		for _, part := range strings.Split(p, "/") {
			if part == ".git" || part == ".convex" || part == "node_modules" {
				return "", invalid("path", "Git, dependency and local backend state paths are forbidden")
			}
		}
	}
	clean := path.Clean(path.Join(base, relative))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", invalid("path", "path escapes the repository")
	}
	return clean, nil
}

func ReservedLocalKey(key string) bool {
	switch key {
	case "CONVEX_DEPLOYMENT", "CONVEX_DEPLOY_KEY", "CONVEX_DEPLOYMENT_TOKEN", "CONVEX_SELF_HOSTED_URL",
		"CONVEX_SELF_HOSTED_ADMIN_KEY", "CONVEX_OVERRIDE_ACCESS_TOKEN", "EVE_CONVEX_TOKEN":
		return true
	}
	return false
}

func validateEnv(field string, env map[string]string, remote bool) error {
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if !envfile.ValidKey(key) {
			return invalid(field, "invalid environment key")
		}
		if ReservedLocalKey(key) || remote && (key == "CONVEX_CLOUD_URL" || key == "CONVEX_SITE_URL") {
			return invalid(field+"."+key, "provider-reserved key")
		}
	}
	return nil
}

func (m *Manifest) validateReferences(field string, env map[string]string, hasEndpoints bool) error {
	for _, key := range slices.Sorted(maps.Keys(env)) {
		expr, err := interpolate.Parse(env[key])
		if err != nil {
			return invalid(field+"."+key, "invalid reference expression")
		}
		for _, ref := range expr.References() {
			p := strings.Split(ref, ".")
			exists := true
			switch p[0] {
			case "workspace":
				exists = p[1] != "port_base" || hasEndpoints
			case "resources":
				_, exists = m.Resources[p[1]]
			case "services":
				s, ok := m.Services[p[1]]
				exists = ok
				if len(p) == 3 {
					exists = exists && s.Port != ""
				} else {
					_, ok := s.Ports[p[3]]
					exists = exists && ok
				}
			}
			if !exists {
				return invalid(field+"."+key, "reference requires an undeclared resource or endpoint")
			}
		}
	}
	return nil
}

type Endpoint struct{ Service, Name, Env string }

// Endpoints defines the creation-time slot order. Sync must use persisted slots
// rather than recomputing this list to renumber existing endpoints.
func (m *Manifest) Endpoints() []Endpoint {
	var endpoints []Endpoint
	for _, id := range slices.Sorted(maps.Keys(m.Services)) {
		s := m.Services[id]
		if s.Port != "" {
			endpoints = append(endpoints, Endpoint{id, "primary", s.Port})
		}
		for name, extra := range s.Ports {
			endpoints = append(endpoints, Endpoint{id, name, extra.Env})
		}
	}
	slices.SortFunc(endpoints, func(a, b Endpoint) int {
		if c := strings.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return endpoints
}

func ParseTTL(value string) (time.Duration, error) {
	if !duration.MatchString(value) {
		return 0, invalid("ttl", "invalid duration")
	}
	n, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil {
		return 0, invalid("ttl", "duration overflow")
	}
	unit := time.Minute
	switch value[len(value)-1] {
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	}
	if n > int64((1<<63-1)/unit) {
		return 0, invalid("ttl", "duration overflow")
	}
	return time.Duration(n) * unit, nil
}

type UserConfig struct{ MinPort, MaxPort int }

func ParseUser(data []byte) (UserConfig, error) {
	var raw struct {
		Version int `toml:"version"`
		Ports   struct {
			Min *int `toml:"min"`
			Max *int `toml:"max"`
		} `toml:"ports"`
	}
	if err := decode(data, &raw); err != nil {
		return UserConfig{}, err
	}
	if raw.Version != 1 {
		return UserConfig{}, invalid("version", "required version is 1")
	}
	c := UserConfig{withDefault(raw.Ports.Min, 20000), withDefault(raw.Ports.Max, 49999)}
	if c.MinPort < 1 || c.MinPort > c.MaxPort || c.MaxPort > 65535 {
		return UserConfig{}, invalid("ports", "require 1 <= min <= max <= 65535")
	}
	return c, nil
}
