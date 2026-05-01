// Package firestore implements store.Store on top of Cloud Firestore for
// the official hosted peersh server.
//
// Document layout (per docs/design/data-model.md, locked in during Phase 5
// planning):
//
//	users/{user_id}                         doc { auth_provider, created_at }
//	users/{user_id}/devices/{device_id}     doc { public_key, kind, display_name, created_at, last_seen_at, fcm_token? }
//	users/{user_id}/pairings/{pairing_id}   doc { mobile_device_id, host_device_id, created_at, last_used_at }
//	users/{user_id}/sessions/{session_id}   doc { mobile_device_id, host_device_id, state, created_at, last_active_at, idle_deadline_at }
//
// PSK records are NOT stored in Firestore (Firebase mode uses Firebase
// Auth ID tokens, not PSK). The Store interface still includes the PSK
// methods; this implementation returns store.ErrNotFound for every PSK
// query so the auth/psk provider degrades gracefully when accidentally
// pointed at a Firestore-backed deployment.
//
// Cost discipline: every method touches at most one or two documents.
// Connection-setup-related reads are bounded by the operator-visible
// budget (≤ ~5 reads + ~2 writes per connection lifecycle); see
// docs/design/architecture.md "Cost discipline".
package firestore
