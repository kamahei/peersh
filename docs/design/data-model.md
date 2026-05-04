# Data Model

This file defines the durable domain entities used by peersh: their identifiers, fields, invariants, lifecycle, and how each pluggable store backend (memory, SQLite, Firestore) maps to them. For the storage interface and pluggability rules, see `architecture.md`.

The schema is intentionally minimal. Fields are added as they are needed, not preemptively.

## Entities

### Device

Represents a physical device participating in peersh: a Windows host running `peershd`, or a mobile device running the app.

- **Identifier.** `device_id`, derived deterministically from the device's public key:
  ```
  device_id = base32(sha256(publicKey)[:10])  // 16-character ASCII
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
  - `fcm_token` (string, optional; reserved for a future host→mobile push path — unused after v2-A)
  - In Firebase mode, the host **also** writes `last_seen_at` to the Realtime Database at `users/{uid}/devices/{deviceId}/last_seen_at` (epoch ms) every 5 minutes; the mobile picker reads from RTDB rather than Firestore.
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
  - In `psk` mode: a server-assigned identifier set by the operator at PSK creation time.
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
  - Created during the pairing flow.
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
  - Output emitted while disconnected is captured in a host-side 256 KiB ring buffer keyed by `session_id`.
  - Idle timeout defaults to 30 minutes.
- **Storage scope.** Sessions are tracked in memory on the host (`peershd`) for the active shell process plus its buffered output. The Firestore `users/{uid}/sessions/{sessionId}` collection is reserved for a future server-side wake-throttling path; after v2-A no client writes to it (wake events flow through Realtime Database `users/{uid}/wake_requests/`).

### PersistedPTY

Represents an interactive ConPTY-backed shell that the host keeps alive across QUIC reconnects so any of the operator's devices can attach to it.

- **Identifier.** `ManagedHandle` (16-character base32, server-issued, opaque).
- **Owner partition.** `Owner = user_id` — the authenticated user_id from the host's PSK `-user` flag or its Firebase UID. Every device that registers under the same user (mobile app, the operator's PC CLI, an additional PC) shares one Owner bucket and can list / reattach to / multi-attach the same PTYs. Cross-user access is impossible because the signaling server already same-user-routes Connect frames; any peer that survives the host's QUIC accept loop is by construction a device of the host's own user. In direct mode (no signaling), Owner is empty and all peers share one bucket — acceptable because direct mode is loopback / dev-only.
- **Fields.**
  - `handle` (string, primary key within an Owner)
  - `owner` (string, == host's user_id)
  - `command` (string, diagnostic — "auto" / "pwsh" / "claude" / ...)
  - `attach_count` (uint, number of streams currently bound)
  - `last_seen` (timestamp, updated on attach/detach transitions)
  - `ring` (1 MiB scrollback buffer)
  - `cwd` (string, last directory observed via OSC 9;9; empty until first prompt)
- **Invariants.**
  - A handle is unique within an Owner; cross-Owner lookup returns "unknown handle" (no existence leak).
  - `attach_count == 0 && now - last_seen > IdleTimeout` ⇒ Sweep evicts on the next pass.
  - `IdleTimeout` defaults to 24 h.
- **Lifecycle.**
  - Created on first `PTYRequest` with empty `reattach_handle`. Pump goroutine starts; ring buffer fills as bytes flow.
  - Multi-attach: subsequent `PTYRequest{reattach_handle}` from any device of the same user adds another sink atomically with a ring-snapshot replay. Existing sinks are not displaced.
  - Detach: a stream close removes its sink; `attach_count` decrements. The PTY itself stays alive until either Drop / KillPTY runs explicitly or the idle TTL elapses.
  - Drop / KillPTY: closes the underlying ConPTY child immediately; the final `PTYExit` frame fans out to whatever sinks remain.
- **Storage scope.** In-memory on the host only. Mobile clients persist their own list of `(server_id, [handles])` in `SecureStore` (`app/lib/state/persisted_pty_handles.dart`) so a fresh launch can offer reattach. The CLI does not persist handles itself — it always starts by listing the host's view.

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
  - `secret_key` is high-entropy (≥ 32 bytes recommended). `peersh-signaling psk add` generates one for you.
  - A revoked PSK must not authenticate.
- **Lifecycle.**
  - Created by the server operator via the `peersh-signaling psk add` admin command.
  - Distributed out-of-band to the user.
  - Revoked by the operator when needed.
- **Storage scope.** Lives only in stores that have a real backing for the `psk` provider — typically SQLite for self-hosting. Not present in `memory` (or only ephemerally for tests) and not used in Firebase mode.
- **Storage shape.** The `secret` is stored as **raw bytes**, not as a hash. HMAC-SHA256 verification needs the secret server-side, so a hash-only scheme cannot work. Trade-off: a server breach exposes every PSK directly. Mitigation: host the SQLite file on disk-encrypted storage; see `docs/deploy/self-hosting.md`.

## Per-backend mapping

### `memory`

- Plain Go maps protected by mutexes, one per entity type.
- No persistence. Fine for development and tests; fine for ephemeral signaling-only deployments where forgetting state on restart is acceptable.

### `sqlite`

- Single-file database (path configurable). Recommended PSK self-host default.
- Tables map roughly 1:1 to entities above (`devices`, `users`, `pairings`, `psk_records`). Sessions live entirely in memory on `peershd`.
- Schema migrations are handled by a small embedded migration runner.

### `firestore`

- Used by Firebase mode for durable state (devices / users / pairings).
- Document layout: `users/{uid}` doc; `users/{uid}/devices/{deviceId}` (device metadata, written by the signaling server's Register handler — not consulted by the mobile picker after v2-A); `users/{uid}/pairings/{pairingId}` (legacy). Plus admin-only `pairing_codes/{code}` (mobile pairing flow) and `ops/budget-state` (cost guardrail).
- `users/{uid}/sessions/{sessionId}` is reserved (no client writes after v2-A).
- Access patterns fit within the cost budget (≤ ~5 reads + ~2 writes per connection lifecycle).
- Security rules enforce per-user isolation: a user can only read/write documents under their own `user_id`. Admin-only paths deny all client access.

### Realtime Database (Firebase mode, v2-A wake path)

Wake events do not live in Firestore — they would consume a real-time listener slot that AGENTS.md's cost discipline forbids on the signaling path. Instead the host opens a single SSE stream to RTDB, which goes to `*.firebasedatabase.app` (not Cloud Run) and idles at gRPC keep-alive cost.

Tree layout under `<project>-default-rtdb`:

```
users/
  {uid}/
    wake_requests/
      {auto-id}: { target_device_id, mobile_device_id?, created_at }
    devices/
      {deviceId}/
        last_seen_at: <epoch ms>
```

- `wake_requests/{rid}` is written by the mobile client on each connect attempt and **deleted by the host immediately after handling**. Crashed hosts leak entries; the listener filter (`target_device_id == self`) keeps stale entries from re-firing.
- `devices/{deviceId}/last_seen_at` is the host's heartbeat (every 5 minutes). The mobile picker reads from this subtree.
- Security rules in `firebase/database.rules.json` allow read/write under `users/{uid}/...` only when `auth.uid == $uid`.
- The mobile app embeds the RTDB region in `app/lib/services/rtdb.dart` (default `asia-southeast1`); change it before building the APK if your project's database lives elsewhere. peershd embeds the same region via `-firebase-rtdb-region` / `PEERSH_BUILD_FIREBASE_RTDB_REGION`.

## What is not in the data model

- **Command output history.** Buffered output during disconnects lives only in a host-side ring buffer for the duration of the session. It is not persisted.
- **Audit logs.** Out of scope for the initial roadmap. May be added later under a clear opt-in.
- **Telemetry / metrics.** The signaling server exposes Prometheus counters at `/metrics` (token-gated; see `docs/deploy/self-hosting.md`).

## Cross-references

- `architecture.md` — interface design (`store.Store`), pluggability rules.
