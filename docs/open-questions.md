# Open Questions

This file consolidates everything in peersh that is not yet decided. It is meant to be edited as questions get answered (move resolved items into the relevant doc and remove them here) and as new questions surface.

The **default assumption** for each item — when one exists — is what implementation should fall back to until the user explicitly decides otherwise.

## Per-phase open questions

### Phase 2 (Signaling Server with PSK Auth)

- **Pairing UX.** How does a mobile device get registered against a Windows device under the same `user_id`?
  - *Default assumption.* QR code flow: the Windows host displays a QR encoding a short-lived pairing token; the mobile device scans it and exchanges the token via the signaling server.
  - *Alternatives.* Plain text token (paste/type), OOB-shared code, account-based association in Firebase mode.
- **`user_id` semantics in PSK mode.** What does a `user_id` actually look like? Server-assigned UUID? Operator-chosen string? Email-like?
  - *Default assumption.* Operator-chosen string at PSK creation time, validated as a non-empty ASCII identifier. Stored as-is in `users` table.
- **PSK generation and distribution.** How are PSKs generated and how does the operator hand the secret to the user safely?
  - *Default assumption.* The signaling server CLI generates a high-entropy random PSK and prints it once to stdout. The operator copies it out of band. The server stores only a hash. (The brief implies storing the secret directly; whether to store the secret or its hash is itself open — see security implications.)
- **WebSocket lifetime.** Does the signaling server keep the WebSocket open after handshake, or close it once endpoints are exchanged?
  - *Default assumption.* Keep it open for the duration of an active session-setup, close after a short idle window. This avoids reconnect overhead on quick re-attempts but doesn't park idle connections forever. Revisit when Phase 5's FCM wake-up adds new constraints.
- **Whether to store the PSK secret directly or only its hash.** Trades off recoverability (operator forgot, can re-display) against compromise blast radius (server breach exposes all secrets).
  - *Default assumption.* Store only the hash; if the operator loses the PSK, regenerate.

### Phase 3 (NAT Hole Punching)

- **Retry aggressiveness.** How many punching attempts and at what intervals?
  - *Default assumption.* A small fixed budget (e.g. 3 attempts over ~10 seconds total) with exponential spacing. Tune based on real-world testing.
- **NAT diagnostics surfaced to the user.** Should the user see what kind of NAT they're behind?
  - *Default assumption.* Internal logs only. Surface to users only when traversal fails, and only at the level of "Direct connection not possible from this network" (no NAT-typology jargon).
- **IPv6-first reliability.** Is IPv6 reliable enough to attempt first by default in the wild?
  - *Default assumption.* Yes, IPv6-first with a short fallback timeout. Revisit if real-world testing shows it consistently slows things down.
- **Birthday-paradox port scanning for symmetric NAT.** Worth implementing or defer?
  - *Default assumption.* Defer to a follow-up issue. Symmetric-NAT-on-both-sides is rare enough for the initial release that the actionable error is acceptable.

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
