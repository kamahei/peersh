# Product Spec

This file describes **what peersh does**, not how it's built. For architecture and implementation, see `architecture.md`.

## Capabilities

1. **PowerShell command execution.** From a paired mobile device, the user runs PowerShell commands on a remote Windows PC and sees streamed `stdout` / `stderr` in real time, rendered with full xterm-style ANSI colours via xterm.dart. The remote shell is a real `pwsh` (or fallback `powershell.exe`) attached to a ConPTY pseudo-console, not a sandboxed subset.
2. **Direct peer-to-peer transport.** All command and output traffic flows directly between the mobile device and the Windows PC over a QUIC connection authenticated with mTLS. No third party can read it in transit.
3. **Signaling-coordinated rendezvous.** A signaling server is used only for connection setup: registering devices, exchanging endpoint candidates. In Firebase mode the signaling WebSocket opens only briefly per session — the host learns it should connect via a Realtime Database wake event delivered out-of-band. Signaling never sees command content.
4. **Dual deployment mode.** The operator can either run the signaling server with PSK auth + SQLite (no Google Cloud required) or with Firebase auth + Firestore for device / pairing state + Realtime Database for cost-efficient wake-event delivery (Google sign-in, multi-PC picker, Cloud Functions for the pairing flow). Both modes coexist in a single APK; per-server-entry choice on the mobile side.
5. **Pluggable authentication.** Three providers ship: `none` (development / LAN), `psk` (HMAC-based pre-shared key, recommended for personal self-hosting), or `firebase` (Firebase Auth ID tokens + optional App Check enforcement).
6. **Pluggable storage.** Three backends ship: in-memory (dev / ephemeral), SQLite (PSK default), Firestore (Firebase mode).
7. **Multiple servers per app.** The mobile app supports adding multiple signaling servers and switching between them; a single APK can hold a mix of PSK and Firebase entries.
8. **Multi-PC picker.** Firebase entries surface a Realtime-Database-backed device picker showing all PCs registered under the signed-in user, ordered by last-seen.
9. **Session continuity.** When a client disconnects (network drop, app backgrounded, deliberate close), the Windows-side `pwsh` process is kept alive within an idle timeout. A reconnecting client presents a `session_id` and is reattached to the same PTY, with cwd and variables intact, plus a 256 KiB ring buffer of output emitted while disconnected.
10. **Auto-reconnect.** The app retries failed connections with exponential backoff (0.5 s → 30 s, max 6 attempts) and shows a transient "Session resumed" banner on success.
11. **Background persistence on Android.** A foreground service holds the OS off the app process while connected so the QUIC keepalive flows even when the screen is off. iOS Background Modes is deferred.
12. **Graceful failure when traversal is impossible.** If both peers are behind NATs that cannot be punched (e.g. symmetric CGNAT on both ends), the user sees a clear error indicating the network is incompatible, not a silent retry loop or a fallback to relayed traffic.
13. **Mobile terminal ergonomics.** Multi-tab terminal (`TerminalTabsScreen`) with reattach UI, special-keys bar (Esc / Ctrl / Tab / arrows / `^C^D^L^Z` / PgUp / PgDn), IME input bottom sheet for IME-heavy languages, wrap-vs-horizontal-scroll toggle, file browser, and a syntax-highlighted text viewer (search + match navigation + copy-all + encoding/size meta).
14. **peershd self-update.** Distribution builds embed a GitHub release repo; `peershd update` fetches the latest release, verifies the SHA-256 sidecar, and atomic-swaps the binary.

## User journeys

### Self-hoster setup

1. The user spins up a small VPS or runs the signaling server on an existing Linux box.
2. They `docker run` (or run the binary directly) `peersh-signaling` with a TOML/YAML config selecting the `psk` auth provider and `sqlite` store.
3. They generate a pre-shared key on the server side and capture a `(user_id, secret)` pair.
4. On their Windows PC, they install and run `peershd`, configured to point at their signaling server's URL with their PSK credentials.
5. On their phone, they open the peersh app, add a server entry pointing at their signaling server with **Auth mode = PSK**, paste the PSK + the device id peershd printed at startup, and tap save.
6. The app shows their Windows device in the device list. Tapping it opens a session screen. They run a command. Output streams back.

### Firebase mode setup

1. Operator provisions a Firebase project (one-time; see `docs/deploy/firebase.md`) and deploys the signaling server in Firebase mode.
2. The user installs `peershd` on their Windows PC and the peersh APK on their phone.
3. On the phone, the user signs in with Google. They tap **Settings → Pair PC** and copy the 6-digit code (or skip this and use `peershd -firebase-login` browser sign-in instead).
4. On the PC: `peershd -pair-code <code>` (or `-firebase-login`). peershd persists a refresh token under `%LOCALAPPDATA%`.
5. The user adds a Firebase server entry in the app. Tapping it shows a device picker with their PC; tap to connect.

### Reconnect after disconnect

1. A user is mid-session on the phone. They get a phone call, the app backgrounds, and connectivity is lost.
2. Some minutes later the user reopens the app.
3. The app reconnects automatically (exponential backoff). On reconnect it presents the previous `session_id`. The Windows side reattaches the existing `pwsh` process and replays buffered output.
4. The user sees the prompt at the same cwd with the same variables and continues working.

### Wake-up via push (Firebase mode only)

1. A user opens the app on their phone. The Windows PC is asleep or has the network adapter idle.
2. The signaling server, on session create, writes a session document; the `onSessionCreated` Cloud Function reads the host device's `fcm_token` and sends a high-priority data message.
3. The Windows device wakes and registers with the signaling server.
4. The phone and Windows PC complete connection setup.

### NAT traversal failure

1. Both endpoints are behind a NAT that does not permit hole punching (typically CGNAT on both sides).
2. After a bounded retry attempt, the app surfaces an error: "Direct connection not possible from this network." The user is not silently routed through any relay; relaying does not exist in peersh.

## Non-functional requirements

- **End-to-end encryption.** QUIC mandates TLS 1.3. mTLS binds connections to keypair-derived device identities. The signaling server operator cannot read command content.
- **Cost discipline (Firebase mode).** Each connection lifecycle consumes ≤ ~5 Firestore reads and ~2 writes. Client-side caching for device info and public keys. No Firestore real-time listeners on the signaling path.
- **Self-hosting simplicity.** Less than five minutes from `docker run` to a working PSK signaling server. No Firebase account required for that path. SQLite by default.
- **Connection-setup latency.** Full setup (signaling rendezvous, hole punching, QUIC handshake) completes in single-digit seconds on a healthy network.
- **Background persistence.** On Android, a foreground service with `dataSync` type plus a persistent notification keeps the QUIC keepalive flowing when backgrounded. iOS Background Modes is deferred.
- **Idle timeout.** Disconnected `pwsh` sessions live for 30 minutes by default; the host's `ptyhost.Manager` reaps idle PTYs on a periodic sweep.
- **Output buffering.** During disconnects, output is captured in a 256 KiB ring buffer; oldest data is dropped when full.
- **Reconnection policy.** App-side reconnect uses exponential backoff (0.5 s → 30 s, capped at 6 attempts). QUIC keepalive interval is 15 s.
- **Protocol stability.** The wire protocol has a public surface. Breaking changes require bumping `protocol_version`. Optional additions are negotiated via `capabilities` strings.

## Out of scope (initial roadmap)

The following are explicitly **not** in scope for the initial roadmap. They may be reconsidered later, but the product, codebase, and documentation should not assume them:

- **TURN / relay servers.** Permanently out of scope as a product principle.
- **OIDC, OAuth2, generic SSO providers.** The `auth.Provider` interface accommodates adding them, but no implementation today.
- **Postgres, MySQL, or other RDBMS stores.** The `store.Store` interface accommodates adding them, but no implementation today.
- **Linux or macOS hosts.** The host side is Windows only.
- **General remote shell (bash/zsh) or non-Windows PowerShell.** PowerShell on Windows only.
- **File transfer, port forwarding, GUI remote desktop.** Out of scope. (A read-only file viewer ships, but no upload / general transfer.)
- **Multi-user shared sessions.** A session belongs to one user.
- **iOS hosted app distribution.** Code-signing pipeline + Apple Developer account out of scope today; iOS source builds work on macOS.
- **Auto-update of the mobile app.** Platform's responsibility (Play Store / App Store / F-Droid).
- **A web UI client.** Mobile only.
