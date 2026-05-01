# Acceptance Criteria

This file lists the project-level invariants and the per-phase done criteria. A phase is not "done" until both the phase-specific criteria and all cross-phase invariants still hold.

For the phase definitions themselves, see `implementation-plan.md`. For task-level breakdowns, see `task-breakdown.md`.

## Cross-phase invariants

These are checked at every phase, not just the one that introduces them.

- **Signaling never carries data.** No path exists by which command content can flow through the signaling server. Reviews flag any change that would route command bytes through `server/`.
- **No relay/TURN.** No code exists that relays QUIC traffic through a third-party server. Failure to traverse NAT surfaces an error; it does not silently fall back.
- **mTLS-derived identity.** All P2P QUIC connections use mTLS. The device's public key fingerprint matches its `device_id`. An attacker without the matching private key cannot impersonate a device, even if they control the signaling server.
- **Pluggable interfaces in their final shape.** `auth.Provider` and `store.Store` exist from Phase 1. Adding a new provider/store means implementing the interface and wiring it in; no `core/` change is required to support the new implementation.
- **No Firebase types in `core/`.** Importing the Firebase SDK from anywhere outside `core/auth/firebase/` and `core/store/firestore/` is a layering violation and must fail review.
- **No per-connection state in package globals.** State that varies per connection is owned by structs that can be constructed and torn down.
- **Protocol stability.** Once Phase 1 ships, breaking changes require a `protocol_version` bump. Optional/additive features go through `capabilities` strings on the Hello messages.
- **Cost discipline (Firebase mode).** Each connection lifecycle in Firebase mode consumes ≤ ~5 Firestore reads + ~2 writes. Client-side caching is in place. No Firestore real-time listeners are used for signaling.
- **OSS readiness.** All first-party code is Apache 2.0. Public APIs, package boundaries, and naming hold up to public review.

## Phase 1 — Same-LAN PoC

- **End-to-end demo passes.** On two machines on the same LAN (or one machine on two ports), `peershd` accepts a connection from `peersh-cli`, completes the `ClientHello` / `ServerHello` exchange, and forwards `ExecRequest` to a real `pwsh` (or fallback `powershell.exe`) process. `Get-Process | Select-Object -First 5` produces correctly streamed `ExecResponse` output.
- **External `net.PacketConn` contract.** A test in `core/transport/` exercises QUIC server and client, each backed by a `net.PacketConn` constructed by the test itself (not by the transport package). The test passes. This is the gate for Phase 3.
- **Workspace builds.** `go work sync` and `go build ./...` succeed at the workspace level.
- **`auth.Provider` and `store.Store` interfaces are in place.** Only `none` and `memory` implementations ship, but the interfaces compile and the runtime wiring goes through them.
- **Self-signed certs work and are clearly dev-only.** `InsecureSkipVerify` is gated by an obvious dev marker (named constant, build tag, or equivalent).
- **README enables a fresh reader.** Following the README on a clean machine reaches a working same-LAN demo.
- **No leakage.** No Firebase, FCM, signaling, NAT, or session-resume code has been introduced.

## Phase 2 — Signaling Server with PSK Auth

- **Two-client signaling rendezvous works.** Two clients on different machines (LAN is acceptable for Phase 2) register against the signaling server, complete PSK auth, exchange endpoint candidates, and establish a direct QUIC connection. They run commands successfully.
- **PSK auth is real.** HMAC-SHA256 over payload + timestamp + nonce. Replay protection against timestamp/nonce reuse within a window. Bad signatures are rejected.
- **SQLite store works as the self-hosting default.** Persistent across server restarts. Schema migration runs cleanly on a fresh DB and on an existing DB.
- **Self-host setup ≤ 5 minutes.** A fresh user with Docker can `docker run` the signaling server, generate a PSK, point `peershd` and `peersh-cli` at it, and reach a working session in under five minutes following `docs/deploy/self-hosting.md`.
- **Rate limiting is in place.** A trivially abusive client cannot saturate the server.
- **Pairing UX decision is made and documented.** Whatever choice is made (QR, token, OOB string), it appears in `docs/deploy/self-hosting.md` and `docs/design/product-spec.md`.
- **No NAT or hole punching has been added.** Endpoint exchange uses cooperative addresses; punching is Phase 3.

## Phase 3 — NAT Hole Punching

- **Real cross-network rendezvous works.** A client on one home network and a client on either another home network or mobile data complete UDP hole punching using STUN-discovered reflexive addresses, and run a working QUIC session.
- **Punched UDP socket is reused for QUIC.** The same `net.PacketConn` that won the punch is what QUIC speaks over. This validates the Phase 1 transport API contract.
- **CGNAT-both-sides surfaces an actionable error.** When traversal cannot succeed, the caller sees a clear error like "Direct connection not possible from this network." No retry loop, no silent degradation, no relayed bytes.
- **IPv6-first / IPv4-fallback policy is implemented and configurable.** The default behavior holds; users can opt out if real-world conditions require it.
- **Timeout and retry budget is bounded.** No connection attempt hangs indefinitely.

## Phase 4 — Flutter App + gomobile Integration

- **gomobile + quic-go spike passes on both platforms.** A minimal proof-of-concept binary using `gomobile bind` runs on a real Android device and a real iOS device with `quic-go` working. This spike happens **before** substantial Flutter app work, so that a wrong choice is discovered early.
- **Real device end-to-end on Android.** The Flutter app on a real Android phone pairs with a self-hosted PSK signaling server and a Windows PC, runs commands, and shows streamed output.
- **Real device end-to-end on iOS.** Same as above on a real iOS device.
- **Multiple servers per app.** Adding a second signaling server entry works; switching between them works; credentials per server are stored separately in platform secure storage.
- **Server discovery via `/.well-known/peersh.json`.** Pointing the app at a hostname picks up the discovery doc and configures connection parameters from it.
- **Method/EventChannel streaming is efficient.** Streamed output renders without dropped frames or large per-chunk overhead.

## Phase 5 — Firebase Auth + FCM Wake-up

- **Google sign-in via FlutterFire works.** A user can sign in on the app, register a Windows device under their account, and see it in their device list.
- **FCM wake-up works.** With the Windows host in a "needs wake-up" state, initiating a session from the phone causes the signaling server to send an FCM push. The Windows host wakes, registers, and the session establishes successfully.
- **App Check is enforced on the official project.** Requests not bearing a valid App Check token (Play Integrity / App Attest) are rejected.
- **Firestore schema is documented and security rules are in place.** A user cannot read or write another user's documents. The schema is documented in `data-model.md` (or a Phase-5-introduced extension thereof).
- **Cost guardrails are wired up.** Budget Alerts, App Engine Daily Spending Limit, auto-disable of the FCM Cloud Function on budget breach.
- **`docs/deploy/firebase.md` exists.** A user hosting their own Firebase project can follow the doc and reach a working setup.
- **Per-connection Firestore budget holds.** Instrumented dev test confirms ≤ ~5 reads + ~2 writes per connection lifecycle.

## Phase 6 — Background Persistence + Session Resumption (Exec)

- **Reattach works (Exec model).** A client disconnects mid-session and reconnects later within the idle window, presenting a `session_id`. The Windows host reattaches the existing `pwsh` process via `pwsh.SessionManager`. cwd and variables are intact.
- **Idle timeout is enforced.** After 30 minutes without a connected client, `Sweep` discards the `pwsh` process. A subsequent reattach with the expired id transparently produces a fresh session.

## Phase 6b — Multi-tab Terminal + PTY Persistence

- **Multi-tab works.** `TerminalTabsScreen` hosts multiple PTYs against one QUIC connection; tabs are kept alive across switches via `IndexedStack`; new tab can spawn or reattach.
- **Server-side PTY persistence works.** `windows/ptyhost.Manager` keeps the ConPTY alive past the QUIC stream that opened it. Closing a tab detaches; the host runs the child until the 30-minute idle TTL elapses.
- **Scrollback ring buffer replays on reattach.** A persisted PTY emits the last ≤ 256 KiB of output as `PTYData` frames before live data resumes.
- **Reattach UI works.** On connect / "+ tab", the user is offered any reattachable PTYs in a bottom-sheet picker. Long-pressing a tab offers "Close (keep alive)" or "Kill PTY".
- **Reattach handles persist locally.** Mobile stores the server-issued handle via `flutter_secure_storage` so the user can leave + reopen the app and rebind.
- **`KillPTY` is reachable from the UI.** Long-press → Kill PTY actually drops the ring buffer + child process.

Remaining Phase 6b acceptance criteria (deferred):

- **Android Foreground Service.** Posts the `connectedDevice` notification; backgrounding does not drop the connection.
- **iOS background persistence.** Background Modes wired up; App Store review implications documented.
- **Mobile auto-reconnect.** Exponential backoff with a clear give-up state.
- **"Session resumed" banner.** Surfaces `reattached = true` in the terminal screen.

## Phase 8 — Interactive PTY Terminal + Session-scoped File API

- **Interactive CLIs work.** `claude`, `codex`, and PowerShell readline all behave correctly on a real Android device: arrow keys / Ctrl+C / Tab completion / resize / Unicode characters all flow end-to-end.
- **PowerShell wrap mode does not corrupt formatting.** When the visible cell count is below 120, the remote PTY is pegged at 120 cols (`remoteColsFor`) so `Get-Process` / `Format-Table` output is not truncated.
- **Scroll mode pins both ends at 120 cols.** xterm's `autoResize: false` + horizontal `SingleChildScrollView` matches the host PTY width exactly.
- **`/.well-known/peersh.json` advertises capabilities.** `pty.v1` and `files.v1` appear; pre-Phase-8 clients still negotiate `protocol_version=2` and never see a PTY frame.
- **OSC 9;9 cwd tracking is robust.** `windows/session.CWDTracker` survives chunk boundaries up to 4 KiB; runaway OSC bodies are truncated and discarded; ST + BEL terminators both work; quoted paths decode.
- **File browser is session-scoped only.** Paths resolve relative to `Session.CWD`; absolute paths or `..` escapes are rejected by `resolveSessionPath`.
- **Text viewer parity.** Search with match navigation; copy-all; syntax highlight toggle (with `flutter_highlighting`); encoding + size + truncated-badge meta line.

## Security hardening (cross-cutting)

- **`/metrics` is fail-closed.** Without `PEERSH_SIGNALING_METRICS_TOKEN`, the endpoint returns 404 — peersh-signaling refuses to leak telemetry to the public internet by default. With the token, `Authorization: Bearer <token>` is required (constant-time compare).
- **PSK secrets stay raw on disk.** Operators are advised to host the SQLite file on disk-encrypted storage (`docs/deploy/self-hosting.md`).

## Phase 7 — Polish, Public Release, and Beyond

Phase 7's deliverables are not yet locked in; acceptance criteria are decided per item during Phase 7 planning. At minimum, anything that does ship in Phase 7 is held to:

- **All cross-phase invariants still hold.**
- **Released artifacts (MSI, Docker image, store builds) are reproducible and documented.**
- **A `SECURITY.md` is in place** covering responsible disclosure.
- **`README.md` reflects the public release** (status moves from "in development" to whatever the public framing is).
