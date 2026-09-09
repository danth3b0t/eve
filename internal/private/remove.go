package private

import (
	"context"
	"errors"
	"os"
)

// RemoveVerified is only for references whose owning operation is durably
// complete. Missing entries are reconciled, never inferred from other I/O errors.
// A changed object is retained. Callers supply the recorded content fingerprint.
func (s *Objects) RemoveVerified(ctx context.Context, ref string, size int64, verify func([]byte) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidRef(ref) || size < 0 || size > MaxBytes || verify == nil {
		return fail()
	}
	if err := s.check(); err != nil {
		return err
	}
	before, err := s.root.Lstat(ref)
	if errors.Is(err, os.ErrNotExist) {
		return s.sync()
	}
	if err != nil {
		return fail()
	}
	data, err := s.Read(ctx, ref, size)
	if err != nil {
		return err
	}
	if int64(len(data)) != size || !verify(data) {
		return fail()
	}
	after, err := s.root.Lstat(ref)
	if err != nil || !stable(before, after) {
		return fail()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Remove(ref); err != nil {
		return fail()
	}
	return s.sync()
}
