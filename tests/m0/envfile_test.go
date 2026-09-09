//go:build linux || darwin

package m0

import (
	"os"
	"strconv"
	"testing"

	"eve/internal/envfile"
)

// M1 writer integration against the established M0 loader, not a substitute for
// the manual M0 baseline or proof of Git/file-publication safety.
func TestNativeEnvfileImages(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" {
		t.Skip("set EVE_M0_NATIVE=1")
	}
	f := newFixture(t)
	v := manualValues(t, "writer-probe")
	for i, app := range []string{"web", "admin"} {
		input := []byte("# existing native config\r\nPORT='3000' # listener\r\nPRIVATE=\"multiline\r\nsecret-sentinel\"\r\nVITE_CONVEX_URL=https://old.convex.cloud\r\n")
		doc, err := envfile.Parse(input)
		must(t, err)
		image, err := doc.Apply(map[string]string{
			"PORT": strconv.Itoa(v.ports[i]), "VITE_CONVEX_URL": v.url, "VITE_CONVEX_SITE_URL": v.siteURL,
		})
		must(t, err)
		f.write(t, "apps/"+app+"/.env.local", string(image))
	}
	p := f.start(t, "run", "dev", "--filter=@eve-m0/web", "--filter=@eve-m0/admin")
	defer p.stop()
	assertFrontends(t, p, v)
	f.unchanged(t)
}
