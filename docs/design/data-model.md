# Data Model

This file defines the durable domain entities used by peersh: their identifiers, fields, invariants, lifecycle, and how each pluggable store backend (memory, SQLite, Firestore) maps to them. For the storage interface and pluggability rules, see `architecture.md`.

The schema below is intentionally minimal for the early phases. Fields are added as they are needed, not preemptively. The Firestore schema in particular is **open** — see `open-questions.md` — and gets locked in during Phase 5 planning.

**Phase 1 scope:** the `store.Store` interface ships with `Device` and `Session` only. `PSKRecord` and `Pairing` (described below) appear in the interface starting in Phase 2 as additive interface extensions; their entries below describe the full eventual shape so callers can see the trajectory.

## Entities

### Device

Represents a physical device participating in peersh: a Windows host running `peershd`, or a mobile device running the app.

- **Identifier.** `device_id`, derived deterministically from the device's public key:
  ```
  device_id = base32(sha256(publicKey)[:16])  // 16-character ASCII
  ```
  Reinstalling produces a new device ID — that is intentional, treat it as a new device.
- **Fields.**
  - `device_id` (string, primary key)
  - `public_key` (bytes; the same key used as the mTLS credential)
  - `owner_user_id` (string; the user this device belongs to)
  - `kind` (enum: `windows_host`, `mobile_client`)
  - `display_name` (string, user-supplied)
  - `created_at` (timestamp)
  - `last_seen_at` (timestamp; updated on each signaling registration)
  - `fcm_token` (string, optional; used in Phase 5+ for wake-up)
- **Invariants.**
  - `device_id` is fully determined by `public_key`. Servers verify this on registration.
  - The same `public_key` cannot be registered to two different `owner_user_id`s.
  - A device cannot claim someone else's `device_id` because the `public_key` would not match.
- **Lifecycle.**
  - Created on first registration against a signaling server (after the user has authenticated and accepted the device).
  - Updated on each successful registration (`last_seen_at`, optionally `fcm_token`).
  - Deleted explicitly by the user (e.g. revoking a device from their account).

### User

Represents an account that owns devices.

- **Identifier.** `user_id`. Provider-dependent:
  - In `firebase` mode: the Firebase UID.
  - In `psk` mode: a server-assigned identifier; exact form is TBD (see `open-questions.md`).
  - In `none` mode: there is no real user; a fixed sentinel user is used.
- **Fields.**
  - `user_id` (string, primary key)
  - `auth_provider` (enum: `none`, `psk`, `firebase`)
  - `created_at` (timestamp)
- **Invariants.**
  - A user belongs to exactly one `auth_provider`. Switching providers means a different user.
- **Lifecycle.**
  - Created lazily on first registration that introduces a new authenticated identity.
  - Deletion semantics are deferred (probably manual / out-of-band in the early phases).

### Pairing

Represents the act of associating a mobile device with a Windows host under the same user.

- **Identifier.** `(user_id, mobile_device_id, host_device_id)` triple.
- **Fields.**
  - `user_id` (string)
  - `mobile_device_id` (string, references Device)
  - `host_device_id` (string, references Device)
  - `created_at` (timestamp)
  - `last_used_at` (timestamp)
- **Invariants.**
  - Both devices must exist and have `owner_user_id == user_id`.
  - A device pair belongs to exactly one user; cross-user pairing is not supported.
- **Lifecycle.**
  - Created during the pairing flow (Phase 2+).
  - Updated each time the pair is used to set up a connection.
  - Deleted when the user removes either device or explicitly unpairs.

### Session

Represents an active or recently-active connection between a paired mobile device and a Windows host.

- **Identifier.** `session_id` (server-assigned, opaque, presented by the client on reconnect to request reattach).
- **Fields.**
  - `session_id` (string, primary key)
  - `user_id` (string)
  - `mobile_device_id` (string)
  - `host_device_id` (string)
  - `state` (enum: `setting_up`, `connected`, `disconnected`, `expired`, `closed`)
  - `created_at` (timestamp)
  - `last_active_at` (timestamp; updated on connection activity)
  - `idle_deadline_at` (timestamp; when the host will discard the underlying `pwsh` process)
- **Invariants.**
  - A `session_id` cannot be reused after it transitions to `expired` or `closed`.
  - On reconnect, a client presenting a `session_id` that exists and belongs to the same user/devices is reattached; otherwise the host creates a new session.
- **Lifecycle.**
  - Created when a client establishes a connection.
  - `state` transitions: `setting_up → connected → disconnected → connected → ... → expired | closed`.
  - Output emitted while disconnected is captured in a host-side ring buffer keyed by `session_id` (Phase 6).
  - Idle timeout default is on the order of 30 minutes; configurability per user/server is open.
- **Storage scope.** Sessions are typically tracked in memory on the host (`peershd`) for the active shell process plus its buffered output. The signaling server may also persist a minimal session record (for FCM wake-up routing in Phase 5+).

### PSKRecord

Represents a `(user_id, secret_key)` pair for the `psk` auth provider.

- **Identifier.** `user_id`.
- **Fields.**
  - `user_id` (string, primary key)
  - `secret_key` (bytes; HMAC-SHA256 key)
  - `display_label` (string, optional)
  - `created_at` (timestamp)
  - `revoked_at` (timestamp, optional)
- **Invariants.**
  - `secret_key` is high-entropy (≥ 32 bytes recommended). Generation is the operator's responsibility; tooling for generation is part of Phase 2.
  - A revoked PSK must not authenticate.
- **Lifecycle.**
  - Created by the server operator (CLI or admin tool, Phase 2).
  - Distributed out-of-band to the user.
  - Revoked by the operator when needed.
- **Storage scope.** Lives only in stores that have a real backing for the `psk` provider — typically SQLite for self-hosting. Not present in `memory` (or only ephemerally for tests) and not used in Firebase mode.
- **Storage shape (Phase 2 resolution).** The `secret` is stored as **raw bytes**, not as a hash. HMAC-SHA256 verification needs the secret server-side, so a hash-only scheme cannot work. Trade-off: a server breach exposes every PSK directly. Mitigation: host the SQLite file on disk-encrypted storage; see `docs/deploy/self-hosting.md`. Future SCRAM-SHA-256 derivation is possible if there is real demand.

## Per-backend mapping

### `memory`

- Plain Go maps protected by mutexes, one per entity type.
- No persistence. Fine for development and tests; fine for ephemeral signaling-only deployments where forgetting state on restart is acceptable.

### `sqlite`

- Single-file database (path configurable). Recommended self-hosting default.
- Tables map roughly 1:1 to entities above (`devices`, `users`, `pairings`, `psk_records`). Sessions may live entirely in memory on `peershd` and not be persisted by the signaling server unless wake-up routing requires it.
- Schema migrations are handled by a small embedded migration runner. The Phase 2 plan defines the initial schema.

### `firestore`

- Used by the official hosted server.
- Document collections roughly mirror the entities; access patterns are designed to fit within the cost budget (≤ ~5 reads + ~2 writes per connection lifecycle).
- Security rules enforce per-user isolation: a user can only read/write documents under their own `user_id`.
- The exact Firestore schema is **open**; it gets locked in during Phase 5 planning. See `open-questions.md`.

## What is not in the data model

- **Command output history.** Buffered output during disconnects lives only in a host-side ring buffer for the duration of the session. It is not persisted.
- **Audit logs.** Out of scope for the initial roadmap. May be added later under a clear opt-in.
- **Telemetry / metrics.** Out of scope until Phase 7's optional Prometheus endpoint on the signaling server.

## Cross-references

- `architecture.md` — interface design (`store.Store`), pluggability rules.
- `implementation-plan.md` — which entities and backends land in which phase.
- `open-questions.md` — exact Firestore schema, PSK distribution UX, idle timeout configurability.
