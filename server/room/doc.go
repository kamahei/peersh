// Package room maintains the in-memory device registry and forwards Connect
// messages between paired devices.
//
// Pairing is implicit in Phase 2: any two devices that have authenticated
// under the same PSK user_id can address each other. Cross-user Connect
// messages are rejected.
package room
