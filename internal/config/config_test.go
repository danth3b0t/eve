package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const web = "version=1\n[services.web]\npath='apps/web'\nenv_file='.env.local'\n"
const backend = "version=1\n[resources.backend]\nprovider='convex'\npath='packages/backend'\nproject='team:project'\n"

func TestExamplesAndDefaults(t *testing.T) {
	for _, name := range []string{"eve.minimal.toml", "eve.monorepo.toml", "eve.shared-root.toml"} {
		data, err := os.ReadFile(filepath.Join("../../examples", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	m, err := Parse([]byte(web))
	if err != nil {
		t.Fatal(err)
	}
	if m.Workspace.PortBlockSize != 100 || m.Services["web"].Host != "localhost" || m.Services["web"].Scheme != "http" || m.Services["web"].AllowTracked || len(m.Endpoints()) != 0 {
		t.Fatal("incorrect defaults or implicit listener allocation")
	}
	m, err = Parse([]byte(backend))
	if err != nil {
		t.Fatal(err)
	}
	r := m.Resources["backend"]
	if r.TTL != "5d" || r.CredentialProfile != "default" || r.EnvFile != ".env.local" || r.Region != "" {
		t.Fatalf("incorrect provider defaults: %#v", r)
	}
}

func TestManifestRejections(t *testing.T) {
	for name, input := range map[string]string{
		"empty": "", "no bindings": "version=1", "unsupported version": strings.Replace(web, "version=1", "version=2", 1),
		"unknown root": "unknown=1\n" + web, "unknown table": "[unknown]\n" + web,
		"case-sensitive field":        strings.Replace(web, "version=1", "Version=1", 1),
		"case-sensitive nested table": strings.Replace(web, "services.web", "Services.web", 1),
		"case-sensitive leaf field":   strings.Replace(web, "env_file=", "ENV_FILE=", 1),
		"unknown leaf":                web + "command='bun dev'\n", "empty unknown leaf table": web + "[services.web.commands]\n",
		"duplicate": web + "path='other'\n", "missing required": strings.Replace(web, "env_file='.env.local'", "", 1),
		"wrong field type": web + "allow_tracked='false'\n", "wrong env type": web + "[services.web.env]\nPORT=42\n",
		"empty port": web + "port=''\n", "invalid key": web + "port='123PORT'\n", "empty host": web + "host=''\n",
		"bad host": web + "host='0.0.0.0'\n", "empty scheme": web + "scheme=''\n", "bad scheme": web + "scheme='ftp'\n",
		"bad id": strings.Replace(web, "services.web", "services.Web", 1), "too-long id": strings.Replace(web, "services.web", "services."+strings.Repeat("a", 49), 1),
		"absolute path": strings.Replace(web, "apps/web", "/apps/web", 1), "escape": strings.Replace(web, "apps/web", "../outside", 1),
		"absolute destination": strings.Replace(web, ".env.local", "/tmp/.env.local", 1), "destination escape": strings.Replace(web, ".env.local", "../../../.env.local", 1),
		"Git destination": strings.Replace(web, ".env.local", "../../.git/config", 1), "dependency source": strings.Replace(web, "apps/web", "node_modules/web", 1),
		"empty resource destination": backend + "env_file=''\n", "empty profile": backend + "credential_profile=''\n",
		"empty region": backend + "region=''\n", "unsupported provider": strings.Replace(backend, "'convex'", "'local'", 1),
		"missing explicit project": strings.Replace(backend, "project='team:project'", "", 1), "bad project": strings.Replace(backend, "team:project", "team/project", 1),
		"bad ttl": backend + "ttl='0d'\n", "overflow ttl": backend + "ttl='999999999999999999999d'\n",
		"primary reserved": web + "[services.web.ports.primary]\nenv='PORT'\n", "missing extra key": web + "[services.web.ports.hmr]\n",
		"unknown extra field": web + "[services.web.ports.hmr]\nenv='HMR_PORT'\nprotocol='tcp'\n",
		"zero block":          web + "[workspace]\nport_block_size=0\n", "large block": web + "[workspace]\nport_block_size=1001\n",
		"block exhausted": web + "port='PORT'\n[services.web.ports.hmr]\nenv='HMR_PORT'\n[workspace]\nport_block_size=1\n",
		"duplicate copy":  web + "[workspace]\ncopy=['.env','.env']\n", "copy escape": web + "[workspace]\ncopy=['../.env']\n",
		"copy local backend": web + "[workspace]\ncopy=['.convex/config.json']\n", "copy brace expansion": web + "[workspace]\ncopy=['{a,b}/.env']\n",
		"unknown referenced service":  web + "[services.web.env]\nURL='${services.missing.url}'\n",
		"missing primary listener":    web + "[services.web.env]\nURL='${services.web.url}'\n",
		"missing additional listener": web + "[services.web.env]\nURL='${services.web.ports.hmr.port}'\n",
		"missing resource":            web + "[services.web.env]\nURL='${resources.missing.url}'\n",
		"no port base":                web + "[services.web.env]\nBASE='${workspace.port_base}'\n",
		"credential expression":       backend + "[resources.backend.env]\nURL='${resources.backend.deploy_key}'\n",
		"env expression":              web + "[services.web.env]\nURL='${env.SECRET}'\n",
		"local reserved key":          web + "[services.web.env]\nCONVEX_DEPLOY_KEY='secret-sentinel'\n",
		"reserved port key":           web + "port='CONVEX_DEPLOYMENT'\n",
		"system remote key":           backend + "[resources.backend.env]\nCONVEX_SITE_URL='https://example.test'\n",
		"management remote key":       backend + "[resources.backend.env]\nEVE_CONVEX_TOKEN='secret-sentinel'\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, err := Parse([]byte(input))
			if err == nil || m != nil {
				t.Fatal("invalid manifest accepted or partially returned")
			}
			if strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("validation disclosed a value")
			}
		})
	}
}

func TestDecodeErrorsRedactAndLocate(t *testing.T) {
	for _, data := range []string{web + "allow_tracked='secret-sentinel'\n", web + "env_file='secret-sentinel'\n", web + "port='secret-sentinel", web + "unknown='secret-sentinel'"} {
		_, err := Parse([]byte(data))
		var e *Error
		if !errors.As(err, &e) || e.Line == 0 || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("missing position or unsafe error: %v", err)
		}
	}
}

func TestEndpointOrderAndValidReferences(t *testing.T) {
	input := web + `port='PORT'
[services.web.ports.hmr]
env='HMR_PORT'
[services.web.env]
BASE='${workspace.port_base}'
HMR='${services.web.ports.hmr.port}'
ESCAPED='$${not.a.reference}'
[services.admin]
path='apps/admin'
env_file='../../.env.local'
port='ADMIN_PORT'
host='::1'
scheme='https'
`
	m, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []Endpoint{{"admin", "primary", "ADMIN_PORT"}, {"web", "hmr", "HMR_PORT"}, {"web", "primary", "PORT"}}
	if !reflect.DeepEqual(m.Endpoints(), want) {
		t.Fatalf("wrong creation-time slot order: %#v", m.Endpoints())
	}
	if got, err := RelativePath(m.Services["admin"].Path, m.Services["admin"].EnvFile); err != nil || got != ".env.local" {
		t.Fatalf("shared-root path: %q, %v", got, err)
	}
}

func TestTTLandUserConfig(t *testing.T) {
	for value, want := range map[string]time.Duration{"1m": time.Minute, "17h": 17 * time.Hour, "5d": 120 * time.Hour, "106751d": 106751 * 24 * time.Hour} {
		if got, err := ParseTTL(value); err != nil || got != want {
			t.Fatalf("TTL %s: %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0m", "01h", "1s", "1h30m", "-1d", "1.5d", "106752d", "9223372036854775807d"} {
		if _, err := ParseTTL(value); err == nil {
			t.Fatalf("invalid TTL %q accepted", value)
		}
	}
	c, err := ParseUser([]byte("version=1"))
	if err != nil || c.MinPort != 20000 || c.MaxPort != 49999 {
		t.Fatal("incorrect user defaults")
	}
	c, err = ParseUser([]byte("version=1\n[ports]\nmin=65535\nmax=65535\n"))
	if err != nil || c.MinPort != 65535 || c.MaxPort != 65535 {
		t.Fatal("inclusive single-port range rejected")
	}
	for _, data := range []string{"", "version=2", "version=1\nunknown=true", "version=1\n[ports]\nmin=0", "version=1\n[ports]\nmin=30000\nmax=20000", "version=1\n[ports]\nmax=65536", "version=1\n[credentials]\ntoken='secret-sentinel'"} {
		if _, err := ParseUser([]byte(data)); err == nil {
			t.Fatal("invalid user config accepted")
		}
	}
	if _, err := Parse([]byte(strings.Repeat("#", MaxBytes+1))); err == nil {
		t.Fatal("oversize manifest accepted")
	}
}

func FuzzManifest(f *testing.F) {
	for _, input := range []string{web, backend, "version=1\n[services]\n", web + "[services.web.env]\nX='${workspace.id}'"} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		m, err := Parse(input)
		if err != nil {
			if m != nil {
				t.Fatal("partial manifest on error")
			}
			return
		}
		if m.Version != 1 || len(m.Services)+len(m.Resources) == 0 || len(m.Endpoints()) > m.Workspace.PortBlockSize {
			t.Fatal("invalid manifest escaped validation")
		}
	})
}

func FuzzRelativePath(f *testing.F) {
	f.Add("apps/web", "../../.env.local")
	f.Add(".", "../outside")
	f.Fuzz(func(t *testing.T, base, relative string) {
		clean, err := RelativePath(base, relative)
		if err != nil {
			return
		}
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			t.Fatal("lexical escape accepted")
		}
	})
}
