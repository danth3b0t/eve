package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagementLogoutDeletesOnlyLocalCredential(t *testing.T) {
	s, _ := fixture(t)
	token := "local-token-no-cloud-delete"
	profile, err := s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, token, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DeleteManagementProfile(t.Context(), "convex", "default")
	if err != nil || result.References != 0 {
		t.Fatalf("logout result: %v %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(s.root, "secrets", profile.CredentialID)); !os.IsNotExist(err) {
		t.Fatal("credential secret retained")
	}
	_, _, err = s.ManagementToken(t.Context(), "convex", "default")
	code(t, err, "E_PROVIDER_AUTH")
	if _, err := s.DeleteManagementProfile(t.Context(), "convex", "default"); err == nil {
		t.Fatal("logout silently repeated absent credential")
	}
}
