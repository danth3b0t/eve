package convex

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingReadCloser) Close() error             { return nil }

func TestUpdateEnvNotFoundIsNotSuccess(t *testing.T) {
	var deployment Deployment
	unmarshalTestDeployment(t, &deployment)
	api, err := New("secret", transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/update_environment_variables" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		return respond(404, ""), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = api.UpdateEnv(t.Context(), deployment, "deploy-key", map[string]string{"SITE_URL": "http://localhost:20000"})
	code(t, err, "E_PROVIDER_NOT_FOUND")
}

func TestAcceptedWriteWithUnreadableBodyStaysAmbiguous(t *testing.T) {
	api, err := New("secret", transport(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/projects/42/deployment":
			return respond(404, ""), nil
		case "POST /v1/projects/42/create_deployment":
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: failingReadCloser{}}, nil
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			return nil, nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.Create(t.Context(), fixtureIntent())
	code(t, err, "E_PROVIDER_AMBIGUOUS")
}

func unmarshalTestDeployment(t *testing.T, out *Deployment) {
	t.Helper()
	if err := json.Unmarshal([]byte(deploymentResponse), out); err != nil {
		t.Fatal(err)
	}
}
