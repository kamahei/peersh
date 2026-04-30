# Implementation Plan

This file is the seven-phase roadmap. Each phase is a separate work session. AI agents working in this repository **default to Plan Mode at the start of each phase** (see `AGENTS.md` and `ai-implementation-guide.md`).

Phases are sequential. Do not start the next phase until the current one is complete and reviewed. Within a phase, the work is broken down further in `task-breakdown.md` (Phase 1 in detail; later phases get expanded when their turn comes).

## Cross-phase invariants

These hold across all phases. Violating them is a regression even if the current phase doesn't directly exercise them.

- **No relay/TURN, ever.** Signaling is connection-setup-only.
- **Pluggable interfaces from day one.** `auth.Provider` and `store.Store` exist from Phase 1 with their final shape, even though only the trivial implementations ship initially.
- **Protocol stability.** Once Phase 1 ships publicly, `protocol_version=1` is locked. Breaking changes require bumping the version. Optional additions go through `capabilities`.
- **OSS readiness from Phase 1.** Don't bake Firebase types into `core/`. Don't put per-connection state in package globals. Code is written for public review.
- **Privacy as a structural property.** The signaling server cannot see command content. mTLS-derived identity prevents impersonation.
- **Cost discipline (Firebase mode).** ≤ ~5 Firestore reads + ~2 writes per connection lifecycle. Client-side caching. No real-time listeners for signaling.
- **External `net.PacketConn` requirement.** `core/transport/` accepts an externally-supplied `net.PacketConn`. This is non-negotiable for Phase 3.

## Phase 1 — Same-LAN PoC

**Goal.** A Go service running on Windows hosts PowerShell. A Go CLI client on another machine on the same LAN can connect via QUIC and execute commands. No auth, no signaling, no NAT — direct IP connection only.

**In scope.**
- Repository layout, `go.work` setup
- Protobuf definitions for `ClientHello` / `ServerHello`, `ExecRequest` / `ExecResponse`
- `core/transport/`: minimal QUIC client/server. **Critically: design the API so an externally-supplied `net.PacketConn` can be used.**
- `core/auth/` interface + `none` implementation
- `core/store/` interface + `memory` implementation
- `windows/pwsh/`: spawn `pwsh.exe -NoExit -Command -`, pipe stdin/stdout/stderr (with fallback to `powershell.exe` if `pwsh` is not on PATH)
- `cmd/peershd`: console app (no Windows Service registration yet)
- `cmd/peersh-cli`: simple REPL client
- Self-signed cert generation; client uses `InsecureSkipVerify` (clearly marked dev-only)
- README setup and verification steps

**Out of scope.** Firebase, signaling, NAT, Flutter, session resumption, background persistence, Windows Service registration.

**Validation.** Manually run server on one machine, client on another (or both on one machine on different ports), execute `Get-Process`, verify streamed output. Plus a small test demonstrating that `quic.Transport` works with an externally-supplied `net.PacketConn`.

## Phase 2 — Signaling Server with PSK Auth

**Goal.** Two clients on different networks (still cooperative, no real NAT yet) can find each other through a signaling server and establish a QUIC connection. PSK authentication is the first real auth provider implemented.

**In scope.**
- `server/`: WebSocket-based signaling server, deployable as a single binary or Docker container
- `core/auth/psk/`: HMAC-SHA256-based PSK provider
- `core/store/sqlite/`: SQLite store (default for self-hosting)
- `core/signaling/`: client side (used by both `peershd` and CLI client)
- Server config via TOML/YAML file plus environment variables
- Pairing flow: how a mobile device gets registered against a Windows device under the same `user_id`
- Endpoint exchange: clients report candidate addresses (LAN IPs are fine initially); server forwards
- Rate limiting basics (per-IP connection rate)
- Dockerfile and `docker-compose.yml` examples for self-hosting
- Documentation: `docs/self-hosting.md`

**Out of scope.** Firebase, NAT hole punching, Flutter, FCM, session resumption.

**Open questions to resolve in planning.** Pairing UX (QR code? token? OOB string?), what `user_id` looks like in PSK mode, how PSKs are generated and distributed, whether to keep the WebSocket open after handshake or close it once endpoints are exchanged.

**Validation.** Two clients on separate machines (same LAN is fine for Phase 2; different networks are fine if cooperative endpoints) connect through the signaling server, complete PSK auth, and execute commands.

## Phase 3 — NAT Hole Punching

**Goal.** Two clients behind real NATs (different home networks, or one on mobile data) can establish a P2P QUIC connection. Connection failures are reported clearly when traversal isn't possible.

**In scope.**
- `core/punching/`: UDP hole punching using `pion/stun` to discover reflexive addresses
- IPv6-first attempt, IPv4 fallback with hole punching
- Reuse of the punched UDP socket for QUIC (validates the Phase 1 transport design)
- Timeout and retry policy
- Clear error reporting back to the caller (CGNAT both sides → "Direct connection not possible from this network")
- Optional: birthday-paradox-style port scanning for symmetric NAT (research; may defer)

**Out of scope.** TURN/relay (explicitly), Flutter, Firebase, FCM.

**Open questions.** How aggressive to be with retries, whether to expose detailed NAT diagnostics to the user, whether IPv6 is reliable enough to attempt first by default.

**Validation.** A real round-trip test from two genuinely different home networks (or one mobile data, one home network) producing a working QUIC connection. CGNAT-both-sides scenario fails with the actionable error.

## Phase 4 — Flutter App + gomobile Integration

**Goal.** A real mobile app (Android + iOS) that pairs with a Windows machine and executes PowerShell. Server URL is user-configurable. Multiple servers can be registered.

**In scope.**
- `mobile-core/`: minimal Go API surface for the app, designed for `gomobile bind`
- **Verify gomobile + quic-go works on iOS and Android (do this early in the phase as a small spike).** This de-risks the entire mobile track. If gomobile is the wrong choice, find out now.
- Flutter app skeleton: pairing screen, server list, device list, session/terminal screen
- Method Channel / EventChannel for Dart ↔ Go communication
- Server discovery via `/.well-known/peersh.json`
- Storage of server list and credentials on device (use platform secure storage)
- Initially target Android (faster iteration), then iOS

**Out of scope.** Firebase auth, FCM, background persistence, session resumption.

**Open questions.** How to model multi-server / multi-device UX, terminal UI fidelity (full ANSI? simple log view?), how Dart receives streaming output from Go efficiently.

**Validation.** Android app on a real device pairs with a self-hosted signaling server (PSK mode) and a Windows PC, runs commands, sees streamed output. Then the same on iOS.

## Phase 5 — Firebase Auth + FCM Wake-up

**Goal.** The official hosted server option works. Users can sign in with Google, register devices under their account, and wake a sleeping Windows machine via FCM.

**In scope.**
- `core/auth/firebase/`: ID token verification
- `core/store/firestore/`: Firestore-backed store
- Firestore schema (devices, pairings, sessions) with security rules enforcing per-user isolation
- Cloud Function: on session create, send FCM to the target Windows device
- App Check integration (Play Integrity / App Attest) to protect the official Firebase project
- FlutterFire integration in the app
- Cost guardrails: Budget Alerts, App Engine Daily Spending Limit, auto-disable Cloud Function on budget breach
- Documentation: `docs/firebase-setup.md` for users hosting their own Firebase

**Out of scope.** Background persistence on mobile, session resumption (next phase).

**Open questions.** Exact Firestore schema, how to handle the case where the Windows device is unreachable even after FCM, how to sync device list across devices.

**Validation.** Sign in on the app with Google. Register a Windows device under the account. With the Windows machine in a "needs wake-up" state, initiate a session from the phone, observe FCM wake, complete connection, run commands.

## Phase 6 — Background Persistence + Session Resumption

**Goal.** The user can disconnect, do other things on their phone, come back later, and resume the same PowerShell session (cwd, variables preserved). The connection survives the app being backgrounded for a reasonable period.

**In scope.**
- Windows side: `SessionManager` that keeps `pwsh.exe` processes alive after disconnect, with idle timeout (default ≈ 30 min) and ring buffer of output during disconnects
- Reattach protocol: client presents a `session_id` on connect; server resumes if it exists, creates if not
- Android: Foreground Service with `connectedDevice` type, persistent notification
- iOS: Background Modes setup (likely `voip` for personal/TestFlight builds; App Store implications discussed)
- Keepalive tuning on the QUIC connection (15s default, NAT mapping refresh)
- App-side automatic reconnect with exponential backoff

**Out of scope.** Anything functionally new beyond persistence/resumption.

**Open questions.** Idle timeout policy (configurable per user? per server?), behavior when output buffer fills during long disconnects, iOS App Store review risks for the chosen Background Modes.

**Validation.** Mid-session, background the app for several minutes, return; verify the same shell, with cwd and variables preserved, plus replay of buffered output emitted during the gap.

## Phase 7 — Polish, Public Release, and Beyond

**Goal.** Make peersh ready for a real public release.

**Possible work items** (prioritize during planning):

- Windows installer (MSI) and service registration via `kardianos/service`
- Auto-update mechanism for the Windows binary
- App Store / Play Store submission
- F-Droid metadata
- One-click deploy templates (Render, Fly.io, Railway)
- Prometheus metrics endpoint on the signaling server
- More auth providers (OIDC?), more stores (Postgres?) if community wants them
- Logo, website, docs site
- Security audit / responsible disclosure process (`SECURITY.md`)

This phase is intentionally loose. Decide what to prioritize based on what we've learned by then.

## How to start a phase

When the user says something like "let's start Phase N":

1. Re-read `project-overview.md`, the relevant phase section above, and the cross-phase invariants at the top of this file.
2. Stay in Plan Mode and produce a plan covering scope, design decisions, open questions, and validation. Do not begin implementation work yet.
3. List open questions and design tradeoffs to discuss before implementing. Treat decisions in this document as **current intent, not frozen** — they can evolve as each phase teaches us things.
4. Wait for plan review. Begin implementation only after the plan is approved.
5. After the phase, propose updates to the relevant docs if assumptions changed.

See `ai-implementation-guide.md` for the AI working mode in more detail.
