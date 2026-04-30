// Package devtls produces development TLS material for peersh.
//
// All exports in this package are dev-only. The grep-able constant
// DevSelfSignedOnly is set to true so that any code path using these helpers is
// trivially identifiable as not-for-production.
package devtls
