package private

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// Key is intentionally opaque in JSON and ordinary debug formatting.
type Key struct{ value [32]byte }

func (k Key) String() string   { return "private.Key" }
func (k Key) GoString() string { return k.String() }
func LoadKey(data []byte) (*Key, error) {
	if len(data) != 32 {
		return nil, fail()
	}
	k := &Key{}
	copy(k.value[:], data)
	return k, nil
}
func frame(h hash.Hash, data []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	h.Write(length[:])
	h.Write(data)
}

// File and Value use distinct, length-framed domains. The same file content
// before/after publication has the same fingerprint, but cannot be substituted
// for another path, workspace, or managed value.
func (k *Key) sum(kind, workspace, path, key string, data []byte) string {
	h := hmac.New(sha256.New, k.value[:])
	for _, part := range []string{"eve-hmac-v1", kind, workspace, path, key} {
		frame(h, []byte(part))
	}
	frame(h, data)
	return hex.EncodeToString(h.Sum(nil))
}
func (k *Key) File(workspace, path string, data []byte) string {
	return k.sum("file", workspace, path, "", data)
}
func (k *Key) Value(workspace, path, key, value string) string {
	return k.sum("value", workspace, path, key, []byte(value))
}
func Equal(a, b string) bool {
	x, e1 := hex.DecodeString(a)
	y, e2 := hex.DecodeString(b)
	return e1 == nil && e2 == nil && len(x) == sha256.Size && len(y) == sha256.Size && hmac.Equal(x, y)
}
