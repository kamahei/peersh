// Package peersh is the gomobile-friendly bridge that the Flutter app uses
// to drive peersh's QUIC + signaling stack.
//
// Public functions are deliberately kept to gomobile-safe signatures
// (string in, string out; no slices, no maps, no errors). Where richer
// shapes are needed, callers should JSON-encode/decode strings on both
// sides.
//
// gomobile bind generates:
//
//   - Android: peersh.aar exporting class peersh.Peersh with static
//     methods Version() and Echo(addr, command).
//   - iOS:     peersh.xcframework exporting Peersh with PeershVersion(),
//     PeershEcho(addr, command) functions.
//
// See scripts/build-mobile-core.{sh,cmd} for the bind invocations.
package peersh
