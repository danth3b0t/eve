package convex

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"eve/internal/domain"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func respond(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

const projectResponse = `{"id":42,"teamId":7,"slug":"m0","teamSlug":"dev-team","devDeploymentName":"default-dev-123","prodDeploymentName":"prod-cow-456"}`
const deploymentResponse = `{"id":123,"name":"calm-cow-456","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":false,"reference":"dev/eve/test/backend","deploymentUrl":"https://calm-cow-456.eu-west-1.convex.cloud","createTime":1800000000000,"expiresAt":1800432000000}`

func fixtureIntent() Intent {
	return Intent{ProjectID: 42, Reference: "dev/eve/test/backend", StartMS: 1_800_000_000_000, ExpiresMS: 1_800_432_000_000, Region: "aws-eu-west-1"}
}

func TestProjectValidationAndDefaultIdentity(t *testing.T) {
	calls := 0
	api, err := New("management-secret", transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer management-secret" {
			t.Fatal("management authorization boundary breached")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/teams/dev-team/projects/m0":
			return respond(200, projectResponse), nil
		case "GET /v1/token_details":
			return respond(200, `{"type":"teamToken","teamId":7}`), nil
		case "GET /v1/deployments/default-dev-123":
			return respond(200, `{"id":10,"name":"default-dev-123","projectId":42,"kind":"cloud","deploymentType":"dev","isDefault":true,"reference":"dev/default","deploymentUrl":"https://default-dev-123.convex.cloud","createTime":1,"expiresAt":0}`), nil
		}
		t.Fatalf("unexpected endpoint %s %s", r.Method, r.URL.Path)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	project, err := api.ValidateProject(t.Context(), "dev-team:m0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.InspectDefault(t.Context(), project.Dev, "dev", project.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("credential validation was retried or used the wrong endpoints")
	}
	for _, bad := range []string{"Dev-Team:m0", "team:m0:extra", "team:m 0", "team.project"} {
		if _, _, ok := ValidProject(bad); ok {
			t.Fatal("unsafe project binding accepted")
		}
	}
}
func TestCreateKeyCanonicalAndPrivateEndToEnd(t *testing.T) {
	in := fixtureIntent()
	sent := []string{}
	api, err := New("secret-sentinel", transport(func(r *http.Request) (*http.Response, error) {
		data := []byte(nil)
		if r.Body != nil {
			data, _ = io.ReadAll(r.Body)
		}
		sent = append(sent, r.Method+" "+r.URL.String()+" "+strings.ToValidUTF8("0000000"+string(data), "nul"))
		assertAuth(r)
		switch {
		case r.URL.Host == "api.convex.dev" && r.Method == "GET" && r.URL.Path == "/v1/projects/42/deployment":
			return respond(404, ""), nil
		case r.URL.Host == "api.convex.dev" && r.Method == "POST" && r.URL.Path == "/v1/projects/42/create_deployment":
			var request CreateDeploymentArgs
			if json.Unmarshal(data, &request) != nil || request.Type != "dev" || request.IsDefault || request.Reference != in.Reference || request.ExpiresAt != in.ExpiresMS || request.Region != in.Region {
				t.Fatalf("wrong create payload: %s", data)
			}
			return respond(200, deploymentResponse), nil
		case r.URL.Host == "api.convex.dev" && r.Method == "POST" && r.URL.Path == "/v1/deployments/calm-cow-456/create_deploy_key":
			return respond(200, `{"deployKey":"dev:calm-cow-456|plain-key"}`), nil
		case r.URL.Host == "api.convex.dev" && r.Method == "GET" && r.URL.Path == "/v1/deployments/calm-cow-456/list_deploy_keys":
			return respond(200, `[{"id":19,"name":"eve-key-g1","expiresAt":1800432000000}]`), nil
		case r.URL.Host == "calm-cow-456.eu-west-1.convex.cloud" && r.Method == "GET" && r.URL.Path == "/api/v1/get_canonical_urls":
			return respond(200, `{"convexCloudUrl":"https://calm-cow-456.eu-west-1.convex.cloud","convexSiteUrl":"https://calm-cow-456.eu-west-1.convex.site"}`), nil
		case r.URL.Host == "calm-cow-456.eu-west-1.convex.cloud" && r.Method == "POST" && r.URL.Path == "/api/v1/update_environment_variables":
			return respond(200, ""), nil
		}
		t.Fatalf("unexpected endpoint %s://%s%s", r.URL.Scheme, r.URL.Host, r.URL.Path)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := api.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	key, err := api.CreateDeployKey(t.Context(), *d, "eve-key-g1", in.ExpiresMS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.VerifyDeployKeyMetadata(t.Context(), *d, *key, in.ExpiresMS); err != nil {
		t.Fatal(err)
	}
	cloud, site, err := api.CanonicalURLs(t.Context(), *d, "nonpublic-key")
	if err != nil {
		t.Fatal(err)
	}
	if cloud != d.URL || !SafeOrigin(site, "site") {
		t.Fatal("canonical outputs incorrect")
	}
	if err = api.UpdateEnv(t.Context(), *d, "nonpublic-key", map[string]string{"SITE_URL": "http://localhost:20000", "ANOTHER": "a"}); err != nil {
		t.Fatal(err)
	}
	for jsonEncoded := range sent {
		if strings.Contains(sent[jsonEncoded], "secret-sentinel") {
			t.Fatal("request logging path built from raw secret bodies")
		}
	}
	if strings.Contains(api.String(), "secret-sentinel") || strings.Contains(key.String(), "plain-key") {
		t.Fatal("formatting leaked credentials")
	}
}
func assertAuth(r *http.Request) {
	want := "Bearer secret-sentinel"
	if r.URL.Host != "api.convex.dev" {
		want = "Convex nonpublic-key"
	}
	if r.Header.Get("Authorization") != want {
		panic("wrong auth scheme/origin")
	}
}
func TestHTTPSafetyContractsAndNoWriteRetry(t *testing.T) {
	for _, kind := range []string{"redirect", "transport", "unauthorized", "server", "malformed", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			api, _ := New("secret-sentinel", transport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == "GET" && r.URL.Path == "/v1/projects/42/deployment" {
					return respond(404, ""), nil
				}
				switch kind {
				case "redirect":
					r := respond(307, "secret-sentinel")
					r.Header.Set("Location", "https://evil.example/secret-sentinel")
					return r, nil
				case "transport":
					return nil, errors.New("secret-sentinel")
				case "unauthorized":
					return respond(401, "secret-sentinel"), nil
				case "server":
					return respond(503, "secret-sentinel"), nil
				case "malformed":
					return respond(200, `{"secret-sentinel":`), nil
				default:
					return respond(200, strings.Repeat("secret-sentinel", 200_000)), nil
				}
			}))
			_, err := api.Create(t.Context(), fixtureIntent())
			if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("unsafe error: %v", err)
			}
			if kind == "transport" {
				code(t, err, "E_PROVIDER_AMBIGUOUS")
			}
			if calls != 2 {
				t.Fatal("non-idempotent provider write was retried or followed")
			}
		})
	}
}
func TestDeleteNeverDeletesWrongIdentity(t *testing.T) {
	owned := deploymentResponse
	for _, kind := range []string{"project", "type", "default", "reference", "expiry", "missing", "failure"} {
		t.Run(kind, func(t *testing.T) {
			body := strings.Replace(owned, `"projectId":42`, "42", 1)
			deleteCalls := 0
			switch kind {
			case "project":
				body = strings.Replace(body, "42", "99", 1)
				kind = "project"
			case "type":
				body = strings.Replace(body, `"deploymentType":"dev"`, `"deploymentType":"prod"`, 1)
			case "default":
				body = strings.Replace(body, `false`, `true`, 1)
			case "reference":
				body = strings.Replace(body, `dev/eve/test/backend`, "dev/eve/other/backend", 1)
			case "expiry":
				body = strings.Replace(body, "1800432000000", "1800432000001", 1)
			}
			api, _ := New("secret", transport(func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" && r.URL.Path == "/v1/deployments/calm-cow-456/delete" {
					deleteCalls++
				}
				if r.Method == "GET" && r.URL.Path == "/v1/deployments/calm-cow-456" {
					if kind == "missing" {
						return respond(404, ""), nil
					}
					if kind == "failure" {
						return respond(503, "bad"), nil
					}
				}
				return respond(200, body), nil
			}))
			var d Deployment
			if json.Unmarshal([]byte(owned), &d) != nil {
				t.Fatal("fixture")
			}
			err := api.Delete(t.Context(), fixtureIntent(), d)
			if err == nil || deleteCalls != 0 {
				t.Fatalf("deletion issue: %v calls=%d body=%s", err, deleteCalls, body)
			}
		})
	}
}
