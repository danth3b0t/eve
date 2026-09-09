// Package platform implements host-local state paths, permissions and locks.
package platform

import (
	"os"
	"path/filepath"
	"runtime"

	"eve/internal/domain"
)

type Paths struct {
	State, Config string
	Isolated      bool // An explicit override does not coordinate with other roots.
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, &domain.Error{Code: "E_STATE_PATH", Message: "cannot determine the user home directory"}
	}
	return pathsFor(runtime.GOOS, home, os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("EVE_STATE_DIR"))
}

func pathsFor(goos, home, xdgState, xdgConfig, override string) (Paths, error) {
	bad := &domain.Error{Code: "E_STATE_PATH", Message: "state/config locations must be absolute local paths on Linux or macOS"}
	if !filepath.IsAbs(home) {
		return Paths{}, bad
	}
	var p Paths
	switch goos {
	case "linux":
		if xdgState == "" {
			xdgState = filepath.Join(home, ".local", "state")
		}
		if xdgConfig == "" {
			xdgConfig = filepath.Join(home, ".config")
		}
		if !filepath.IsAbs(xdgState) || !filepath.IsAbs(xdgConfig) {
			return Paths{}, bad
		}
		p.State = filepath.Join(xdgState, "eve")
		p.Config = filepath.Join(xdgConfig, "eve", "config.toml")
	case "darwin":
		p.State = filepath.Join(home, "Library", "Application Support", "eve")
		p.Config = filepath.Join(p.State, "config.toml")
	default:
		return Paths{}, bad
	}
	if override != "" {
		if !filepath.IsAbs(override) {
			return Paths{}, bad
		}
		p.State, p.Isolated = filepath.Clean(override), true
	}
	return p, nil
}
