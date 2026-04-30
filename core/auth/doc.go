// Package auth defines the auth.Provider interface used by the signaling
// server (Phase 2+) and by peershd. Implementations live in subpackages:
// auth/none, auth/psk (Phase 2), auth/firebase (Phase 5).
//
// Firebase types must not appear in this package — the firebase provider lives
// entirely under auth/firebase to keep the dependency boundary clean.
package auth
