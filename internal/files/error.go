package files

import (
	"errors"
	"fmt"

	"eve/internal/domain"
	"eve/internal/envfile"
)

// Add file context only to reviewed value-free diagnostics. Never wrap an
// arbitrary filesystem, subprocess or parser error that may include contents.
func fileError(name string, err error) error {
	var env *envfile.Error
	if errors.As(err, &env) {
		return failure(env.Code, fmt.Sprintf("%s (key=%q, lines=%v)", env.Reason, env.Key, env.Lines), name)
	}
	var d *domain.Error
	if errors.As(err, &d) && d.Path == "" {
		copy := *d
		copy.Path = name
		return &copy
	}
	return err
}
