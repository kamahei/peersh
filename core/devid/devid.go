package devid

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
)

// Length is the canonical peersh device-ID length, in characters.
//
// device_id = base32(sha256(publicKey)[:16])  // 16 ASCII characters
//
// 16 raw bytes through standard (RFC 4648) base32 yields 16 padded
// characters. We strip the no-padding portion and rely on the fixed input
// length to keep the output shape stable.
const Length = 16

// rawBytes is how many bytes of the SHA-256 digest we keep before encoding.
const rawBytes = 10 // 10 bytes -> 16 base32 characters with no padding.

// Derive returns the peersh device ID for a given public key.
//
// The output is 16 ASCII characters using the standard base32 alphabet (A-Z,
// 2-7), upper-case, no padding. The same input always produces the same
// device ID, by design.
func Derive(publicKey []byte) string {
	if len(publicKey) == 0 {
		return ""
	}
	sum := sha256.Sum256(publicKey)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:rawBytes])
}

// ErrMismatch is returned by Verify when the supplied public key does not
// produce the supplied device ID.
var ErrMismatch = errors.New("devid: public key does not match device id")

// Verify confirms that a public key produces the given device ID. Returns
// ErrMismatch when the IDs differ; returns a wrapped error if id is the
// wrong shape.
func Verify(id string, publicKey []byte) error {
	if len(id) != Length {
		return fmt.Errorf("devid: device id has wrong length: got %d want %d", len(id), Length)
	}
	if Derive(publicKey) != id {
		return ErrMismatch
	}
	return nil
}
