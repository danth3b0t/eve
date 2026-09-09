//go:build linux || darwin

package m0

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedContracts(t *testing.T) {
	for name, checksum := range map[string]string{
		"management-openapi.json": "8107cc743e0b297c42262ec40374b44294f9fbe959d0e61ba9b7beb8237627da",
		"deployment-openapi.json": "b4e1df4dfdb02dbfb401a623feaa6339495c63f88c24acb7dc5b69603a66fa3d",
	} {
		data, err := os.ReadFile(filepath.Join("../../testdata/provider-contracts/convex", name))
		must(t, err)
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != checksum || !json.Valid(data) {
			t.Fatalf("%s changed: explicitly review wire types, origin and checksum before updating", name)
		}
	}
}

func TestCredentialOrigins(t *testing.T) {
	for _, origin := range []string{"https://calm-cow-456.convex.cloud", "https://calm-cow-456.eu-west-1.convex.cloud"} {
		if !safeOrigin(origin, "cloud") {
			t.Errorf("rejected documented origin %s", origin)
		}
	}
	for _, origin := range []string{
		"http://calm-cow-456.convex.cloud", "https://convex.cloud",
		"https://calm-cow-456.convex.cloud.evil.example", "https://user@calm-cow-456.convex.cloud",
		"https://calm-cow-456.convex.cloud:443", "https://calm-cow-456.convex.cloud/path",
		"https://calm-cow-456.convex.cloud?key=secret", "https://calm-cow-456.convex.cloud#fragment",
		"https://calm-cow-456.convex.site", "https://127.0.0.1",
	} {
		api := newCloudAPI("management-secret")
		api.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			t.Fatal("credential request reached transport for unapproved origin")
			return nil, nil
		})
		if _, err := api.deployed(deployment{URL: origin}, "deploy-secret", "GET", "get_canonical_urls", nil, nil); err == nil {
			t.Errorf("accepted unsafe credential origin %s", origin)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestHTTPAuthorizationAndUnsafeResponses(t *testing.T) {
	api := newCloudAPI("management-secret")
	api.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		want := "Bearer management-secret"
		if r.URL.Host != "api.convex.dev" {
			want = "Convex deployment-secret"
		}
		if r.Header.Get("Authorization") != want {
			t.Fatal("incorrect authorization boundary")
		}
		return response(200, `{}`), nil
	})
	_, err := api.management("GET", "/token_details", nil, nil)
	must(t, err)
	_, err = api.deployed(deployment{URL: "https://calm-cow-456.convex.cloud"}, "deployment-secret", "GET", "get_canonical_urls", nil, nil)
	must(t, err)

	for _, kind := range []string{"redirect", "transport", "unauthorized", "server", "malformed", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			api := newCloudAPI("secret-sentinel")
			calls := 0
			api.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				switch kind {
				case "redirect":
					resp := response(307, "secret-sentinel")
					resp.Header.Set("Location", "https://evil.example/secret-sentinel")
					return resp, nil
				case "transport":
					return nil, errors.New("secret-sentinel")
				case "unauthorized":
					return response(401, "secret-sentinel"), nil
				case "server":
					return response(503, "secret-sentinel"), nil
				case "malformed":
					return response(200, `{"secret-sentinel":`), nil
				default:
					return response(200, strings.Repeat("secret-sentinel", 200_000)), nil
				}
			})
			var out deployment
			_, err := api.management("POST", "/projects/42/create_deployment", createRequest{
				Type: "dev", IsDefault: false, Reference: "dev/eve/test", ExpiresAt: 1_800_432_000_000,
			}, &out)
			if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("missing or unredacted error: %v", err)
			}
			if calls != 1 {
				t.Fatalf("non-idempotent write repeated or credential redirect followed: %d requests", calls)
			}
		})
	}

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		api.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			if !errors.Is(r.Context().Err(), context.Canceled) {
				t.Fatal("cancellation not propagated")
			}
			return nil, r.Context().Err()
		})
		if _, err := api.request(ctx, "GET", managementOrigin+"/token_details", "Bearer secret", nil, nil); err == nil {
			t.Fatal("cancelled request succeeded")
		}
	})
}

// Independent response fixture: numeric IDs, required boolean discriminator,
// millisecond timestamps, and a regional URL from the reviewed wire schema.
const ownedResponse = `{
  "id":123,"name":"calm-cow-456","projectId":42,"kind":"cloud",
  "deploymentType":"dev","isDefault":false,"reference":"dev/eve/test/backend",
  "deploymentUrl":"https://calm-cow-456.eu-west-1.convex.cloud",
  "createTime":1800000000000,"expiresAt":1800432000000,"futureAdditiveField":true
}`

func TestCleanupOwnershipAndFalseAbsence(t *testing.T) {
	intent := creation{ProjectID: 42, Reference: "dev/eve/test/backend", Start: 1_800_000_000_000, ExpiresAt: 1_800_432_000_000}
	var owned deployment
	must(t, json.Unmarshal([]byte(ownedResponse), &owned))
	must(t, verifyDeployment(owned, intent))
	for _, scenario := range []string{
		"owned", "already absent", "unknown absent", "reference changed", "wrong project", "production", "default",
		"missing default discriminator", "expiry changed", "old creation", "exact ID changed", "unauthorized", "forbidden", "server", "delete forbidden", "post-delete offline",
	} {
		t.Run(scenario, func(t *testing.T) {
			api := newCloudAPI("secret-sentinel")
			deleted := false
			api.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.convex.dev" || r.Header.Get("Authorization") != "Bearer secret-sentinel" {
					t.Fatal("cleanup escaped management boundary")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/projects/42/deployment":
					if r.URL.Query().Get("reference") != "dev/eve/test/backend" {
						t.Fatal("lookup broadened beyond exact reference")
					}
					switch scenario {
					case "unauthorized":
						return response(401, "secret-sentinel"), nil
					case "forbidden":
						return response(403, "secret-sentinel"), nil
					case "server":
						return response(503, "secret-sentinel"), nil
					case "already absent", "unknown absent", "reference changed":
						return response(404, ""), nil
					}
					body := ownedResponse
					switch scenario {
					case "wrong project":
						body = strings.Replace(body, `"projectId":42`, `"projectId":99`, 1)
					case "production":
						body = strings.Replace(body, `"deploymentType":"dev"`, `"deploymentType":"prod"`, 1)
					case "default":
						body = strings.Replace(body, `"isDefault":false`, `"isDefault":true`, 1)
					case "missing default discriminator":
						body = strings.Replace(body, `"isDefault":false,`, ``, 1)
					case "expiry changed":
						body = strings.Replace(body, `1800432000000`, `1800432001000`, 1)
					case "old creation":
						body = strings.Replace(body, `1800000000000`, `1700000000000`, 1)
					}
					return response(200, body), nil
				case "GET /v1/deployments/calm-cow-456":
					if deleted && scenario == "post-delete offline" {
						return response(503, "secret-sentinel"), nil
					}
					if deleted || scenario == "already absent" {
						return response(404, ""), nil
					}
					if scenario == "exact ID changed" {
						return response(200, strings.Replace(ownedResponse, `"id":123`, `"id":999`, 1)), nil
					}
					return response(200, ownedResponse), nil
				case "POST /v1/deployments/calm-cow-456/delete":
					if scenario != "owned" && scenario != "delete forbidden" && scenario != "post-delete offline" {
						t.Fatal("deletion attempted without verified ownership")
					}
					if deleted {
						t.Fatal("deletion repeated")
					}
					if scenario == "delete forbidden" {
						return response(403, "secret-sentinel"), nil
					}
					deleted = true
					return response(200, ""), nil
				default:
					t.Fatalf("unexpected operation (project deletion is forbidden): %s %s", r.Method, r.URL.Path)
					return nil, nil
				}
			})
			journal, err := os.CreateTemp(t.TempDir(), "journal-")
			must(t, err)
			defer journal.Close()
			p := cloudProbe{api: api, intent: intent, observed: &owned, journal: journal, key: "secret-sentinel"}
			if scenario == "unknown absent" {
				p.observed = nil
			}
			err = p.cleanup()
			wantSuccess := scenario == "owned" || scenario == "already absent"
			if (err == nil) != wantSuccess || p.cleaned != wantSuccess {
				t.Fatalf("cleanup result: %v, cleaned=%v", err, p.cleaned)
			}
			if err != nil && strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("cleanup diagnostic leaked a secret")
			}
			data, err := os.ReadFile(journal.Name())
			must(t, err)
			if strings.Contains(string(data), "secret-sentinel") {
				t.Fatal("evidence file contains credential material")
			}
			if scenario == "owned" && !deleted {
				t.Fatal("owned deployment not deleted")
			}
		})
	}
}
