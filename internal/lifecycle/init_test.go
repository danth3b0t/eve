package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitRefusesExistingOrUnsupportedManifest(t *testing.T) {
	r := repositoryFixture(t)
	_, err := ProposeInit(t.Context(), r.client, r.root, InitOptions{})
	errorCode(t, err, "E_INIT_EXISTS")
	if err := os.Remove(filepath.Join(r.root, "eve.toml")); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "drop manifest")
	_, err = ProposeInit(t.Context(), r.client, r.root, InitOptions{})
	errorCode(t, err, "E_INIT_DISCOVERY")
}
