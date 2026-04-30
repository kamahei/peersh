// Package devid implements the peersh device identity rule:
//
//	device_id = base32(sha256(publicKey)[:16])  // 16-character ASCII
//
// The same public key serves both as identity and as the credential for
// mTLS. Reinstalling the app produces a new device ID — that is intentional.
package devid
