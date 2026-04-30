// Package memory implements store.Store with in-memory mutex-protected maps.
// It is used in Phase 1, in tests, and in ephemeral signaling-only
// deployments where forgetting state on restart is acceptable.
package memory
