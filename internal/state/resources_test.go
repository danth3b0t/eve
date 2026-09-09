package state

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/config"
	"github.com/google/uuid"
)

const providerManifest = `version = 1
[resources.backend]
provider = "convex"
path = "."
project = "dev-team:m0"
`

func TestResourceIntentAndCredentialLifecycle(t *testing.T) {
	s, r := fixture(t)
	id := uuid.NewString()
	lock, err := s.LockWorkspace(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = lock.BeginCreate(t.Context(), CreateRequest{RepositoryID: r.ID, Branch: "provider", Path: filepath.Join(filepath.Dir(r.SourcePath), id), HeadOID: strings.Repeat("a", 40), Manifest: []byte(providerManifest), Ports: config.UserConfig{MinPort: 20000, MaxPort: 49999}})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := lock.Resources(t.Context())
	if err != nil || len(resources) != 1 {
		t.Fatal(err)
	}
	res := resources[0]
	if res.RemoteReference == "" || res.IntendedExpiresAtMS == 0 || res.State != "planned" {
		t.Fatalf("invalid intent %+v", res)
	}
	res.RemoteID = "123"
	res.RemoteName = "cow-123"
	res.RemoteProjectID = "42"
	res.ExpiresAtMS = res.IntendedExpiresAtMS
	if err := lock.MarkResource(t.Context(), res.ID, "provisioned"); err != nil {
		t.Fatal(err)
	}
	credential, gen, err := lock.StartDeployKey(t.Context(), res, fmt.Sprintf("key-g%d", 1))
	if err != nil || gen != 1 {
		t.Fatal(err)
	}
	if err := s.StoreDeploymentKey(t.Context(), credential, "dev:cow-123|secret"); err != nil {
		t.Fatal(err)
	}
	res.CredentialID = credential
	res.KeyGeneration = gen
	res.State = "configured"
	if err := lock.RecordResource(t.Context(), res); err != nil {
		t.Fatal(err)
	}
	resources, err = lock.Resources(t.Context())
	if err != nil || resources[0].State != "configured" || resources[0].CredentialID != credential {
		t.Fatal("resource not recorded")
	}
	secret, err := lock.DeployKeyCredential(t.Context(), resources[0])
	if err != nil || secret != "dev:cow-123|secret" {
		t.Fatal("secret missing")
	}
	var metadata string
	if err := s.db.QueryRow(`SELECT group_concat(metadata_json) FROM credential_objects`).Scan(&metadata); err != nil || strings.Contains(metadata, secret) || strings.Contains(metadata, "|secret") {
		t.Fatal("deployment key leaked into SQL")
	}
}
