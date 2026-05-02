// Package devtls produces development TLS material for peersh.
//
// Production peersh code paths (peershd, cli signaling mode, mobile-core
// signaling sessions) use core/transport/peertls instead — a self-signed
// mTLS layer bound to ed25519-derived device_ids. devtls remains in
// tree only for transport-package tests; new code should not import it.
//
// The grep-able constant DevSelfSignedOnly is set to true so that any
// code path using these helpers is trivially identifiable as
// not-for-production.
package devtls
