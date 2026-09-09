package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagementProfileIntentAndVerification(t *testing.T) {
	s, _ := fixture(t)
	token := "management-secret-for-test"
	first, err := s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, token, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.CredentialID == "" || first.LastValidatedAtMS != time.Unix(100, 0).UnixMilli() {
		t.Fatal("invalid profile metadata")
	}
	var ordered string
	for _, query := range []string{`SELECT group_concat(metadata_json) FROM credential_objects`, `SELECT group_concat(team_slug) FROM credential_profiles`} {
		_ = s.db.QueryRow(query).Scan(&ordered)
		if strings.Contains(ordered, token) {
			t.Fatal("token leaked into SQL")
		}
	}
	info, err := os.Stat(filepath.Join(s.root, "secrets", first.CredentialID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential object is not private")
	}
	got, raw, err := s.ManagementToken(t.Context(), "convex", "default")
	if err != nil || raw != token || got.CredentialID != first.CredentialID {
		t.Fatal("credential round trip failed")
	}
	second, err := s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, token, time.Unix(200, 0))
	if err != nil || second.CredentialID != first.CredentialID {
		t.Fatal("same-token login did not reconcile")
	}
	code(t, func() error {
		_, err := s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, "new-token", time.Now())
		return err
	}(), "E_CREDENTIAL_PROFILE")
	gotAgain, still, err := s.ManagementToken(t.Context(), "convex", "default")
	if err != nil || still != token || gotAgain.CredentialID != first.CredentialID {
		t.Fatal("failed login rotated token")
	}
}
func TestManagementProfileInterruptedRecovery(t *testing.T) {
	for _, kind := range []string{"missing", "partial", "same-size"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := fixture(t)
			token := "stable-token"
			profile, err := s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, token, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(s.root, "secrets", profile.CredentialID)
			switch kind {
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			case "same-size":
				if err := os.WriteFile(name, []byte(strings.Repeat("x", len(token))), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.StoreManagementProfile(t.Context(), "convex", "default", "dev-team", 7, token, time.Now())
			if kind == "missing" {
				if err != nil {
					t.Fatal(err)
				}
				_, raw, err := s.ManagementToken(t.Context(), "convex", "default")
				if err != nil || raw != token {
					t.Fatal("complete intent not restored exactly")
				}
				return
			}
			code(t, err, "E_CREDENTIAL_PROFILE")
			data, _ := os.ReadFile(name)
			if string(data) == token && kind != "missing" {
				t.Fatal("profile token was silently rotated")
			}
		})
	}
}
func TestCancelledProfileCreatesNoIntent(t *testing.T) {
	s, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.StoreManagementProfile(ctx, "convex", "default", "dev-team", 7, "token", time.Now())
	if err != context.Canceled {
		t.Fatalf("cancelled profile: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM credential_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled login left credential intent")
	}
}
