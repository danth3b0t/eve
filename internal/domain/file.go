package domain

import "os"

// FileIdentity freezes preimage metadata for a future replacement check. It is
// not write/delete authorization and must be combined with content HMAC/policy.
type FileIdentity struct {
	Identity  string
	Mode      os.FileMode
	Size      int64
	ModTimeNS int64
}
