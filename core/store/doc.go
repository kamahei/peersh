// Package store defines the store.Store interface used by the signaling
// server and by peershd. Implementations live in subpackages: store/memory,
// store/sqlite (Phase 2), store/firestore (Phase 5).
//
// Firebase / Firestore types must not appear in this package.
package store
