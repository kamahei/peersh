// Package transport is a thin QUIC wrapper that accepts an externally-supplied
// net.PacketConn. Phase 3 (NAT hole punching) requires reusing the punched UDP
// socket as the QUIC transport; that capability is unlocked by the
// caller-owned PacketConn requirement on this package.
package transport
