// Package peertls is the production TLS layer for peersh QUIC connections.
//
// peersh's identity model already binds the device id to a public key:
//
//	device_id = base32(sha256(publicKey)[:10])
//
// peertls uses that property directly. There is no certificate authority
// and no PKI: each side presents a self-signed X.509 certificate whose
// private key is the device's long-lived ed25519 keypair, and each side
// validates the peer by:
//
//  1. confirming the peer cert is a single self-signed leaf with an
//     ed25519 public key, and
//  2. (client side) confirming devid.Derive(peer pub key) matches the
//     target device_id supplied to ClientTLSConfig.
//
// The server side intentionally accepts any pubkey-bound client cert.
// The application layer is expected to cross-check the authenticated peer
// device_id against signaling-supplied identity (room membership,
// Connect.from_device_id) before granting access.
//
// peertls replaces core/transport/devtls for production code paths.
// devtls remains in tree for tests and the developer-only direct-dial
// CLI / mobile spike screen, where there is no signaling channel to
// supply an expected target device_id.
package peertls
