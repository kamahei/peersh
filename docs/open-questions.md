# Open Questions

This file consolidates everything in peersh that is not yet decided. It is meant to be edited as questions get answered (move resolved items into the relevant doc and remove them here) and as new questions surface.

The **default assumption** for each item — when one exists — is what implementation should fall back to until the user explicitly decides otherwise.

## Per-phase open questions

### Phase 2 (Signaling Server with PSK Auth) — resolved

All Phase 2 open questions were resolved during Phase 2 planning. Resolutions live in:

- **Pairing UX → implicit (Phase 2)** — devices that share a PSK `user_id` are automatically paired; explicit token / QR comes with the mobile app in Phase 4. See `docs/product-spec.md` and `docs/self-hosting.md`.
- **`user_id` semantics → operator-chosen string** at PSK creation time. See `docs/data-model.md` (User entity) and `docs/self-hosting.md`.
- **PSK generation / distribution → server CLI generates 32-byte random PSK, prints once, raw secret stored.** See `docs/self-hosting.md` and `server/admin`.
- **WebSocket lifetime → kept open while a session-setup is active.** Closed when the client disconnects.
- **PSK storage shape → raw bytes.** HMAC verification needs the secret; hash-only is structurally impossible. Trade-off (server-breach exposes PSKs) is documented in `docs/self-hosting.md` with disk-encryption recommendation.

### Phase 3 (NAT Hole Punching) — resolved

All Phase 3 open questions were resolved during Phase 3 planning.

- **Retry aggressiveness → 5 punch packets at 200 ms intervals; 1 dial attempt per candidate at 2 s timeout; 4 candidates max, ~10 s total budget.** See `core/punching` and `docs/architecture.md`.
- **NAT diagnostics surfaced → internal logs only.** STUN-discovered srflx, candidates tried, and per-candidate dial outcomes log at INFO/DEBUG. Users see exactly one error line: "Direct connection not possible from this network." (`punching.ErrTraversalFailed`).
- **IPv6-first reliability → yes, IPv6 SRFLX → IPv4 SRFLX → IPv6 HOST → IPv4 HOST.** Implemented in `punching.SortCandidates`. Revisit if real-world testing shows it consistently slows connection setup.
- **Birthday-paradox port scanning → deferred.** Phase 3 surfaces the actionable error for symmetric-NAT-on-both-sides; revisit only if there is real demand.

### Phase 4 (Flutter App + gomobile)

- **Multi-server / multi-device UX.** How do we present multiple signaling servers and the devices under each?
  - *Default assumption.* A two-level list: servers at the top level, devices nested under each server. Tapping a device opens its session screen.
- **Terminal UI fidelity.** Full ANSI terminal emulation, or simple log view?
  - *Default assumption.* Simple monospaced log view for the first iteration, with `xterm.js`-equivalent ANSI parsing only if early users find the simple view insufficient.
- **Streaming output efficiency from Go to Dart.** Method Channel per chunk is too chatty.
  - *Default assumption.* Use EventChannel with chunked binary frames; group small writes within a few-millisecond window into a single channel send.

### Phase 5 (Firebase Auth + FCM)

- **Exact Firestore schema.** The schema is intentionally not pinned in `data-model.md`; Phase 5 planning decides it.
  - *Default assumption.* Collections roughly: `users/{uid}`, `users/{uid}/devices/{deviceId}`, `users/{uid}/pairings/{pairingId}`, `users/{uid}/sessions/{sessionId}`. Designed for the read/write budget (≤ ~5 reads + ~2 writes per lifecycle). Revisit when Phase 5 plan is in.
- **Behavior when the Windows device is unreachable even after FCM.** How long do we wait before giving up, and what does the user see?
  - *Default assumption.* A bounded wait (e.g. 30 seconds), then a clear error: "Could not reach your Windows device. It may be offline."
- **Device list sync across multiple mobile devices.** A user with two phones expects both to see the same registered Windows devices.
  - *Default assumption.* Read from Firestore on app start with client-side caching. No real-time listener (cost discipline).

### Phase 6 (Background Persistence + Session Resumption)

- **Idle timeout policy.** Per-user? Per-server? Hardcoded default?
  - *Default assumption.* Hardcoded ~30 min default initially; consider configurability after we see how users actually use sessions.
- **Output buffer overflow during long disconnects.** What happens when the ring fills?
  - *Default assumption.* Truncate oldest, keep newest. Mark in the replay that older output was dropped.
- **iOS App Store review risks.** The likely-needed Background Mode is `voip`, which is not strictly the right semantic for a remote shell. App Store reviewers may push back.
  - *Default assumption.* Ship to TestFlight first under `voip`. Document the App Store risk in `docs/firebase-setup.md` (or a Phase 6 doc TBD). Be ready to switch to a different mode or accept that public iOS distribution requires a different approach.

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
- When the user makes a decision: move the resolved item from this file into the appropriate doc (`product-spec.md`, `architecture.md`, `data-model.md`, etc.) with the chosen answer. Remove it from here.
- When a new question surfaces during planning or implementation: add it here with a default assumption, so future readers know how to proceed if a decision hasn't been made yet.
