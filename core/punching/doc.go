// Package punching implements the Phase 3 NAT-traversal helpers used by
// peershd and peersh-cli.
//
//   - Discover queries a STUN server to learn the caller's reflexive (public)
//     UDP address.
//   - Punch sends a brief burst of magic-byte sentinel packets at a peer's
//     reflexive address to install a NAT mapping locally so that the peer's
//     subsequent QUIC dial passes through.
//   - SortCandidates orders endpoint candidates in preferred dial order.
//
// The package is intentionally thin: there is no ICE state machine, no TURN,
// and no parallel candidate race. Phase 3 is "the simplest thing that works
// behind real home NATs"; ICE-style sophistication arrives only if real-world
// testing demands it.
package punching
