//go:build linux || darwin

package m0

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

type browserResult struct {
	Success bool
	Data    struct{ Origin, Result string }
	Error   any `json:"error"`
}

func browserConfigJSON(t *testing.T, session, url string) (map[string]string, bool) {
	t.Helper()
	dir, err := exec.LookPath("agent-browser")
	if err != nil {
		t.Skip("agent-browser required for explicit browser validation")
	}
	defer exec.Command(dir, "--session", session, "close").Run()
	for _, action := range [][]string{{"open", url}, {"wait", "--load", "networkidle"}, {"eval", "--json", `document.querySelector("#config").textContent`}} {
		cmd := exec.Command(dir, append([]string{"--session", session}, action...)...)
		output, _ := cmd.CombinedOutput()
		if action[0] == "eval" {
			var result browserResult
			if json.Unmarshal(output, &result) != nil || !result.Success || result.Data.Origin != url+"/" {
				return nil, false
			}
			return parseBrowserConfig(result.Data.Result)
		}
	}
	return nil, false
}
func parseBrowserConfig(text string) (map[string]string, bool) {
	values := map[string]string{}
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return nil, false
	}
	return values, true
}

// This uses a real browser process to execute the app JavaScript. It does not
// count an HTTP response body or a subprocess environment dump as browser evidence.
func TestNativeBrowserSlice(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" || os.Getenv("EVE_M0_BROWSER") != "1" {
		t.Skip("set EVE_M0_NATIVE=1 and EVE_M0_BROWSER=1; requires pinned app tools plus agent-browser")
	}
	f := newFixture(t)
	v := manualValues(t, "browser")
	f.configureFrontends(t, v)
	process := f.start(t, "run", "dev", "--filter=@eve-m0/web", "--filter=@eve-m0/admin")
	assertFrontends(t, process, v)
	deadline := time.Now().Add(30 * time.Second)
	for _, app := range []struct {
		name string
		port int
	}{{"web", v.ports[0]}, {"admin", v.ports[1]}} {
		session := "eve-m0-" + app.name
		url := fmt.Sprintf("http://127.0.0.1:%d", app.port)
		var got map[string]string
		var ok bool
		for time.Now().Before(deadline) {
			got, ok = browserConfigJSON(t, session, url)
			if ok && got["url"] == v.url && got["siteUrl"] == v.siteURL {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !ok || got["url"] != v.url || got["siteUrl"] != v.siteURL {
			t.Fatalf("browser %s saw inconsistent public configuration: %v", app.name, got)
		}
	}
	process.stop()
	f.unchanged(t)
}
