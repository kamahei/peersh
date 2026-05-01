# Open Questions

This file consolidates everything in peersh that is not yet decided. It is meant to be edited as questions get answered (move resolved items into the relevant doc and remove them here) and as new questions surface.

The **default assumption** for each item — when one exists — is what implementation should fall back to until the user explicitly decides otherwise.

## Per-phase open questions

### Phase 2 (Signaling Server with PSK Auth) — resolved

All Phase 2 open questions were resolved during Phase 2 planning. Resolutions live in:

- **Pairing UX → implicit (Phase 2)** — devices that share a PSK `user_id` are automatically paired; explicit token / QR comes with the mobile app in Phase 4. See `docs/design/product-spec.md` and `docs/deploy/self-hosting.md`.
- **`user_id` semantics → operator-chosen string** at PSK creation time. See `docs/design/data-model.md` (User entity) and `docs/deploy/self-hosting.md`.
- **PSK generation / distribution → server CLI generates 32-byte random PSK, prints once, raw secret stored.** See `docs/deploy/self-hosting.md` and `server/admin`.
- **WebSocket lifetime → kept open while a session-setup is active.** Closed when the client disconnects.
- **PSK storage shape → raw bytes.** HMAC verification needs the secret; hash-only is structurally impossible. Trade-off (server-breach exposes PSKs) is documented in `docs/deploy/self-hosting.md` with disk-encryption recommendation.

### Phase 3 (NAT Hole Punching) — resolved

All Phase 3 open questions were resolved during Phase 3 planning.

- **Retry aggressiveness → 5 punch packets at 200 ms intervals; 1 dial attempt per candidate at 2 s timeout; 4 candidates max, ~10 s total budget.** See `core/punching` and `docs/design/architecture.md`.
- **NAT diagnostics surfaced → internal logs only.** STUN-discovered srflx, candidates tried, and per-candidate dial outcomes log at INFO/DEBUG. Users see exactly one error line: "Direct connection not possible from this network." (`punching.ErrTraversalFailed`).
- **IPv6-first reliability → yes, IPv6 SRFLX → IPv4 SRFLX → IPv6 HOST → IPv4 HOST.** Implemented in `punching.SortCandidates`. Revisit if real-world testing shows it consistently slows connection setup.
- **Birthday-paradox port scanning → deferred.** Phase 3 surfaces the actionable error for symmetric-NAT-on-both-sides; revisit only if there is real demand.

### Phase 4 (Flutter App + gomobile) — resolved

All Phase 4 open questions were resolved during Phase 4b planning:

- **Multi-server / multi-device UX → two-level list.** ServersScreen → TerminalScreen on tap; Phase 4b ships single-target-per-server (the user pastes the host's device_id into the server entry). Richer multi-device-per-server discovery waits for Firestore (Phase 5).
- **Terminal UI fidelity** — Phase 4b shipped a simple monospaced log view; **Phase 8 superseded it with full xterm.dart 4.x ANSI rendering** (the LogView class is gone). The simple-log path is no longer reachable.
- **Streaming Go → Dart → EventChannel + base64-encoded chunk events.** One `EventChannel` shared across sessions; events tagged with `sessionId`. The platform message codec carries `ByteArray` natively on Android (Uint8List on Dart side) so no further encoding is required.

### Phase 5 (Firebase Auth + FCM) — partially resolved

Phase 5's server-side shipped (`core/auth/firebase`, `core/store/firestore`, Cloud Function, `firestore.rules`). The mobile-app FlutterFire integration and App Check enforcement are deferred to Phase 5b alongside the iOS device validation.

- **Firestore schema → locked.** Collections: `users/{uid}`, `users/{uid}/devices/{deviceId}`, `users/{uid}/pairings/{pairingId}`, `users/{uid}/sessions/{sessionId}`. PSK records are NOT stored in Firebase mode. Documented in `core/store/firestore/doc.go` and `docs/design/data-model.md`.
- **Behavior when the Windows device is unreachable even after FCM** — Phase 5b decision (mobile-side error UX). Default still applies: bounded wait (~30 s), then "Could not reach your Windows device. It may be offline."
- **Device list sync across multiple mobile devices** — Phase 5b decision (FlutterFire-side). Default still applies: Firestore read on app start with client-side caching, no real-time listener.

### Phase 6 / 6b (Background Persistence + Session Resumption) — partially resolved

Phase 6 and Phase 6b Tier 1+2 are shipped: `pwsh.SessionManager` for the legacy Exec model, plus the new `ptyhost.Manager` with 256 KiB ring buffer + 30 min TTL + `PTYReattachAck` + `ListPTYs` / `KillPTY` RPCs and the matching mobile reattach UI. The remaining items belong to the deferred-by-design Phase 6b items (auto-reconnect + Foreground Service / iOS BG Modes).

- **Idle timeout policy → 30 min hardcoded** (`pwsh.DefaultIdleTimeout`, `ptyhost.IdleTimeout`). Per-user configurability remains a future polish item.
- **Ring-buffer overflow during long disconnects → resolved.** `ptyhost.RingBufferSize = 256 KiB`, oldest bytes drop on overflow; replay reflects whatever the client most recently saw.
- **iOS App Store review risks** — still deferred (no iOS device available remotely).

### Phase 8 (Interactive PTY Terminal + session-scoped file API) — resolved

- **Default shell → `auto`**, resolved by `windows/shell.Resolve` (pwsh > powershell > SystemRoot fallback). Operator override via PTYRequest.command (e.g. `claude` direct).
- **PTY data multiplexing → `PTYFrame` oneof.** Single QUIC stream per PTY; Input/Resize go up, Data/Exit/ReattachAck come down.
- **PowerShell formatter narrow-width corruption → `remoteColsFor` with kHorizontalScrollCols = 120.** In wrap mode against a pwsh-like shell, the remote PTY is pegged at ≥ 120 cols even when the visible cell count is smaller, so Get-Process / Format-Table outputs are never truncated.
- **OSC 9;9 cwd tracking** — body cap 4 KiB, BEL + ST terminators, chunk-boundary tolerance, body-truncated invalidation. State machine ported from peersh\.
- **File-browser scope → session-scoped only.** No operator-configured roots, no bookmarks: peersh's narrower threat model intentionally restricts file access to the cwd a real user is typing in.
- **`/metrics` exposure → token-gated.** `PEERSH_SIGNALING_METRICS_TOKEN` empty → 404 (fail-closed); set → `Authorization: Bearer <token>` + ConstantTimeCompare.

## Things that are likely to change

These are not bugs — they are decisions documented as **current direction, not frozen specs**. Expect them to evolve as each phase teaches us:

- **Exact Protobuf schema** (especially around session resumption fields in Phase 6).
- **Pairing UX** (QR / token / account-based).
- **Mobile background-persistence approach** (especially on iOS).
- **Whether `gomobile` remains the right choice.** A Rust + `flutter_rust_bridge` alternative may be worth re-evaluating if `gomobile` causes friction during Phase 4.
- **The Firestore schema** (see Phase 5 above).
- **The set of auth providers.** OIDC may eventually be requested.

When friction with any of these surfaces during a phase, raise it in that phase's plan rather than working around it silently.

## How to use this file

- When starting a phase: read the relevant section above and surface the open questions in your plan.
- When the user makes a decision: move the resolved item from this file into the appropriate doc (`design/product-spec.md`, `design/architecture.md`, `design/data-model.md`, etc.) with the chosen answer. Remove it from here.
- When a new question surfaces during planning or implementation: add it here with a default assumption, so future readers know how to proceed if a decision hasn't been made yet.
