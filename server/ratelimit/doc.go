// Package ratelimit provides per-IP, per-user, and per-device token buckets
// for the signaling server.
//
// Implementation is intentionally small: a map of buckets, mutex-guarded,
// pruned periodically by a background sweeper. No external dependency.
package ratelimit
