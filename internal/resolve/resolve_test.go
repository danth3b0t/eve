package resolve

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"eve/internal/config"
	"eve/internal/envfile"
)

func parse(t *testing.T, text string) *config.Manifest {
	t.Helper()
	m, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConnectedResolutionAndNativeImage(t *testing.T) {
	m := parse(t, `version=1
[services.web]
path='apps/web'
env_file='../../.env.local'
port='WEB_PORT'
host='::1'
allow_tracked=true
[services.web.ports.hmr]
env='HMR_PORT'
[services.web.env]
BACKEND='${resources.backend.url}'
PRIMARY_URL='${services.web.url}'
SHARED='${workspace.slug}'
[services.admin]
path='apps/admin'
env_file='../../.env.local'
port='ADMIN_PORT'
[services.admin.env]
SHARED='same'
[resources.backend]
provider='convex'
path='packages/backend'
project='team:project'
[resources.backend.env]
SITE_URL='${services.web.url}'
LABEL='ready for ${workspace.branch}'
`)
	base := 24000
	in := Inputs{Workspace: Workspace{ID: "uuid", Slug: "same", Branch: "feature/work", PortBase: &base}, Ports: map[Endpoint]int{
		{"web", "primary"}: 24042, {"web", "hmr"}: 24007, {"admin", "primary"}: 24009,
	}, Resources: map[string]ResourceOutputs{"backend": {URL: "https://calm-cow-456.convex.cloud"}}}
	result, err := Resolve(m, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != ".env.local" {
		t.Fatal("shared destination aliases did not merge")
	}
	file := result.Files[0]
	if file.AllowTracked {
		t.Fatal("tracked-file consent requires every owner")
	}
	if !reflect.DeepEqual(file.Owners["SHARED"], []string{"services.admin.env.SHARED", "services.web.env.SHARED"}) {
		t.Fatal("identical desired values lost co-ownership")
	}
	if result.RemoteEnv["backend"]["SITE_URL"] != "http://[::1]:24042" || result.RemoteEnv["backend"]["LABEL"] != "ready for feature/work" {
		t.Fatal("local/remote references or JSON value semantics wrong")
	}
	doc, err := envfile.Parse([]byte("# developer config\nWEB_PORT=3000 # web\nPRIVATE=secret-sentinel\nUNRELATED=3000\n"))
	if err != nil {
		t.Fatal(err)
	}
	image, err := doc.Apply(file.Values)
	if err != nil {
		t.Fatal(err)
	}
	want := "# developer config\nWEB_PORT=24042 # web\nPRIVATE=secret-sentinel\nUNRELATED=3000\nADMIN_PORT=24009\nBACKEND=https://calm-cow-456.convex.cloud\nHMR_PORT=24007\nPRIMARY_URL=http://[::1]:24042\nSHARED=same\n"
	if string(image) != want {
		t.Fatalf("wrong native image:\ngot  %q\nwant %q", image, want)
	}
}

func TestConflictingSharedKeyReportsAllOwners(t *testing.T) {
	m := parse(t, `version=1
[services.a]
path='a'
env_file='../.env.local'
[services.a.env]
URL='https://one.example'
[services.b]
path='b'
env_file='../.env.local'
[services.b.env]
URL='https://two.example'
[services.c]
path='.'
env_file='.env.local'
[services.c.env]
URL='https://one.example'
`)
	result, err := Resolve(m, Inputs{})
	var e *Error
	if result != nil || !errors.As(err, &e) || e.Code != "E_NATIVE_ENV_CONFLICT" || e.Path != ".env.local" || e.Key != "URL" {
		t.Fatalf("missing conflict: %v", err)
	}
	if !reflect.DeepEqual(e.Owners, []string{"services.a.env.URL", "services.b.env.URL", "services.c.env.URL"}) {
		t.Fatalf("missing conflicting source locations: %#v", e.Owners)
	}
	if strings.Contains(err.Error(), "https://") {
		t.Fatal("conflict diagnostic exposed env values")
	}
}

func TestAllocatedPortsAreNotRenumberedOrInvented(t *testing.T) {
	m := parse(t, "version=1\n[workspace]\nport_block_size=2\n[services.web]\npath='.'\nenv_file='.env.local'\nport='PORT'\n[services.web.ports.hmr]\nenv='HMR_PORT'\n")
	for _, tc := range []struct {
		name  string
		base  *int
		ports map[Endpoint]int
	}{
		{"missing block", nil, map[Endpoint]int{{"web", "primary"}: 20000}},
		{"missing endpoint", new(20000), map[Endpoint]int{{"web", "primary"}: 20000}},
		{"duplicate endpoint port", new(20000), map[Endpoint]int{{"web", "primary"}: 20000, {"web", "hmr"}: 20000}},
		{"out of block", new(20000), map[Endpoint]int{{"web", "primary"}: 20000, {"web", "hmr"}: 20002}},
		{"zero base", new(0), nil}, {"overflow block", new(65535), nil}, {"huge base", new(int(^uint(0) >> 1)), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Resolve(m, Inputs{Workspace: Workspace{PortBase: tc.base}, Ports: tc.ports})
			if err == nil || result != nil {
				t.Fatal("invalid allocation accepted")
			}
		})
	}
	result, err := Resolve(m, Inputs{Workspace: Workspace{PortBase: new(65534)}, Ports: map[Endpoint]int{{"web", "primary"}: 65535, {"web", "hmr"}: 65534}})
	if err != nil || result.Files[0].Values["PORT"] != "65535" || result.Files[0].Values["HMR_PORT"] != "65534" {
		t.Fatalf("existing boundary allocations moved: %v", err)
	}
}

func TestResolutionFailsWithoutPartialResultsOrSecretOutput(t *testing.T) {
	for name, fragment := range map[string]string{"missing workspace value": "URL='${workspace.id}'", "unsupported local value": "URL='secret sentinel'", "escaped reference not portable": "URL='$${workspace.id}'"} {
		t.Run(name, func(t *testing.T) {
			m := parse(t, "version=1\n[services.web]\npath='.'\nenv_file='.env.local'\n[services.web.env]\nA='safe'\n"+fragment)
			result, err := Resolve(m, Inputs{})
			if err == nil || result != nil || strings.Contains(err.Error(), "secret sentinel") {
				t.Fatalf("missing, unsafe or partial diagnostic: %v", err)
			}
		})
	}
	m := parse(t, "version=1\n[services.web]\npath='.'\nenv_file='.env.local'\n[services.web.env]\nPRIVATE='secret-sentinel'\n")
	result, err := Resolve(m, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "secret-sentinel") {
		t.Fatal("resolved configuration accidentally exposed by JSON serialization")
	}
	m = parse(t, "version=1\n[resources.backend]\nprovider='convex'\npath='.'\nproject='team:project'\n[resources.backend.env]\nURL='${resources.backend.site_url}'\n")
	for _, resources := range []map[string]ResourceOutputs{nil, {"backend": {URL: "https://calm-cow-456.convex.cloud"}}} {
		result, err := Resolve(m, Inputs{Resources: resources})
		if err == nil || result != nil {
			t.Fatal("missing provider output accepted")
		}
	}
}
