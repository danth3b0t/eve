package interpolate

import (
	"strings"
	"testing"
)

func TestExpressions(t *testing.T) {
	values := map[string]string{
		"workspace.id": "uuid", "workspace.slug": "feature-uuid", "workspace.branch": "feature/work",
		"workspace.port_base": "23000", "services.web.port": "23001", "services.web.url": "http://[::1]:23001",
		"services.web.ports.hmr.port": "23002", "resources.backend.url": "https://calm-cow-456.convex.cloud",
		"resources.backend.site_url": "https://calm-cow-456.convex.site", "resources.backend.deployment": "dev:calm-cow-456",
		"resources.backend.name": "calm-cow-456", "resources.backend.reference": "dev/eve/uuid/backend",
	}
	for ref, want := range values {
		expr, err := Parse("prefix/${" + ref + "}/suffix")
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.Evaluate(values)
		if err != nil || got != "prefix/"+want+"/suffix" {
			t.Fatalf("%s: %q, %v", ref, got, err)
		}
	}
	for input, want := range map[string]string{
		"": "", "plain $HOME $(never-run)": "plain $HOME $(never-run)",
		"$${workspace.id}-${workspace.id}": "${workspace.id}-uuid",
		"$${resources.backend.deploy_key}": "${resources.backend.deploy_key}",
	} {
		expr, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.Evaluate(values)
		if err != nil || got != want {
			t.Fatalf("%q: got %q, want %q, %v", input, got, want, err)
		}
	}
	expr, _ := Parse("${workspace.branch}")
	got, err := expr.Evaluate(map[string]string{"workspace.branch": "${resources.backend.deploy_key}"})
	if err != nil || got != "${resources.backend.deploy_key}" {
		t.Fatal("reference values must never be reparsed")
	}
}

func TestUnavailableOrUnsupportedReferences(t *testing.T) {
	for _, input := range []string{
		"${resources.backend.deploy_key}", "${env.SECRET}", "${services.web.env.PORT}", "${services.web.ports.primary.port}",
		"${workspace.path}", "${resources.backend.region}", "${services.Web.port}", "${services.web.url.extra}",
		"${workspace.id", "${}", "${${workspace.id}}", "${workspace.id }", "\x00", "\xff",
	} {
		if _, err := Parse("secret-sentinel" + input); err == nil {
			t.Fatalf("unsupported expression accepted: %q", input)
		} else if strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("expression error disclosed a literal")
		}
	}
	expr, err := Parse("secret-sentinel/${services.web.port}")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := expr.Evaluate(nil); err == nil || value != "" || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("missing value leaked partial output or succeeded")
	}
}

func TestExpressionBounds(t *testing.T) {
	if _, err := Parse(strings.Repeat("a", MaxBytes+1)); err == nil {
		t.Fatal("oversized expression accepted")
	}
	expr, _ := Parse("${workspace.branch}${workspace.branch}")
	value, err := expr.Evaluate(map[string]string{"workspace.branch": strings.Repeat("a", MaxBytes)})
	if err == nil || value != "" {
		t.Fatal("oversized result accepted or partially returned")
	}
}

func FuzzExpression(f *testing.F) {
	for _, value := range []string{"", "${workspace.id}", "$${resources.backend.deploy_key}", "${services.web.ports.hmr.port}", "${", "plain"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, input string) {
		expr, err := Parse(input)
		if err != nil {
			return
		}
		values := map[string]string{}
		for _, ref := range expr.References() {
			values[ref] = "fixed"
		}
		result, err := expr.Evaluate(values)
		if err != nil {
			t.Fatal("resolution with present, bounded values failed")
		}
		if len(result) > MaxBytes {
			t.Fatal("unbounded result")
		}
		if len(expr.References()) != 0 {
			if partial, err := expr.Evaluate(nil); err == nil || partial != "" {
				t.Fatal("unresolved reference accepted or partially returned")
			}
		}
	})
}
