# Product Spec

This file describes **what peersh does**, not how it's built. For architecture and implementation, see `architecture.md` and `implementation-plan.md`.

## Capabilities

The product, taken across all phases, delivers the following capabilities:

1. **PowerShell command execution.** From a paired mobile device, the user can run PowerShell commands on a remote Windows PC and see streamed `stdout` and `stderr` in real time. The remote shell is a real `pwsh` (or fallback `powershell.exe`) process, not a sandboxed subset.
2. **Direct peer-to-peer transport.** All command and output traffic flows directly between the mobile device and the Windows PC over a QUIC connection authenticated with mTLS. No third party can read it in transit.
3. **Signaling-coordinated rendezvous.** A signaling server is used only for connection setup: registering devices, exchanging endpoint candidates, optionally waking the Windows host via push notification. Signaling never sees command content.
4. **Dual deployment mode.** The user can either (a) use the official hosted signaling server backed by Firebase, or (b) run their own signaling server (single binary or Docker) and configure the mobile app to point at it.
5. **Pluggable authentication.** The signaling server can be configured to use one of three auth providers: `none` (development / LAN), `psk` (HMAC-based pre-shared key, recommended for personal self-hosting), or `firebase` (Firebase Auth ID tokens, used by the official hosted option). Phase 2 ships `none` and `psk`; `firebase` arrives in Phase 5.
6. **Pluggable storage.** The signaling server can be configured to back its state with one of three stores: in-memory (dev / ephemeral), SQLite (recommended self-hosting default), or Firestore (official hosted option).
7. **Multiple servers per app.** The mobile app supports adding multiple signaling servers and switching between them. A user can be on the official hosted server and on a self-hosted server simultaneously.
8. **Session continuity.** When a client disconnects (network drop, app backgrounded, deliberate close), the Windows-side `pwsh` process is kept alive within an idle timeout. A reconnecting client presents a `session_id` and is reattached to the same shell, with cwd and variables intact, plus a ring buffer of output emitted while disconnected.
9. **Best-effort background persistence on mobile.** When backgrounded by the user, the mobile app holds the connection open within OS constraints (Foreground Service on Android; Background Modes on iOS).
10. **Graceful failure when traversal is impossible.** If both peers are behind NATs that cannot be punched (e.g. symmetric CGNAT on both ends), the user sees a clear error indicating the network is incompatible, not a silent retry loop or a fallback to relayed traffic.

## User journeys

### Self-hoster setup

1. The user spins up a small VPS or runs the signaling server on an existing Linux box.
2. They `docker run` (or run the binary directly) `peersh-signaling` with a TOML/YAML config selecting the `psk` auth provider and `sqlite` store.
3. They generate a pre-shared key on the server side and capture a `(user_id, secret)` pair.
4. On their Windows PC, they install and run `peershd`, configured to point at their signaling server's URL with their PSK credentials.
5. On their phone, they open the peersh app, add a server entry pointing at their signaling server, and pair via QR code or token (mobile UX arrives in Phase 4; in the meantime the CLI is the only client and uses **implicit pairing** — any two devices that authenticate under the same PSK `user_id` are automatically paired).
6. The app shows their Windows device in the device list. Tapping it opens a session screen. They run a command. Output streams back.

### Casual user setup (official hosted)

1. The user installs `peershd` on their Windows PC and the peersh app on their phone.
2. On the phone they sign in with Google via Firebase Auth.
3. The Windows installer (or first-run flow) prompts them to authenticate the device against their account.
4. The Windows PC appears in their device list on the phone. They tap it and run a command.

### Reconnect after disconnect

1. A user is mid-session on the phone. They get a phone call, the app backgrounds, and connectivity is lost.
2. Some minutes later the user reopens the app.
3. The app reconnects automatically (exponential backoff). On reconnect it presents the previous `session_id`. The Windows side reattaches the existing `pwsh` process and replays buffered output.
4. The user sees the prompt at the same cwd with the same variables and continues working.

### Wake-up via push (official hosted only)

1. A user opens the app on their phone. The Windows PC is asleep or has the network adapter idle.
2. The signaling server, on session create, sends an FCM push to the registered Windows device.
3. The Windows device wakes and registers with the signaling server.
4. The phone and Windows PC complete connection setup.

### NAT traversal failure

1. Both endpoints are behind a NAT that does not permit hole punching (typically CGNAT on both sides).
2. After a bounded retry attempt, the app surfaces an error: "Direct connection not possible from this network." The user is not silently routed through any relay; relaying does not exist in peersh.

## Non-functional requirements

- **End-to-end encryption.** QUIC mandates TLS 1.3. mTLS is used to bind connections to keypair-derived device identities. The signaling server operator must not be able to read command content.
- **Cost discipline (official hosted).** Each connection lifecycle should consume at most a handful of Firestore reads and writes (target: ≤ ~5 reads + ~2 writes). Client-side caching is required for device info and public keys. No Firestore real-time listeners are used for the signaling path; signaling uses WebSocket with in-memory server-side state.
- **Self-hosting simplicity.** The bar is "less than five minutes from `docker run` to a working signaling server." No Firebase account required. No external database required (SQLite by default).
- **Connection-setup latency.** The full setup (signaling rendezvous, hole punching, QUIC handshake) should complete fast enough to feel like opening a remote shell — single-digit seconds on a healthy network.
- **Background persistence.** On Android, the app uses a Foreground Service with `connectedDevice` type plus a persistent notification. On iOS, the app uses Background Modes (likely `voip` for personal/TestFlight builds; App Store implications are an open question).
- **Idle timeout.** Disconnected `pwsh` sessions live for a bounded idle period (default in the order of 30 minutes; whether this is configurable per user or per server is open).
- **Output buffering.** During disconnects, output emitted by the running shell is captured in a bounded ring buffer. Behavior when the buffer fills is open (truncate-oldest is the likely default).
- **Reconnection policy.** App-side reconnect uses exponential backoff. QUIC keepalive interval is on the order of 15 seconds to refresh NAT mappings.
- **Protocol stability.** Once Phase 1 ships, the wire protocol has a public surface. Breaking changes require bumping `protocol_version`. Optional additions are negotiated via `capabilities` strings.

## Out of scope (initial roadmap)

The following are explicitly **not** in scope for the initial roadmap. They may be reconsidered later, but the product, codebase, and documentation should not assume them:

- **TURN / relay servers.** Permanently out of scope as a product principle.
- **OIDC, OAuth2, generic SSO providers.** The `auth.Provider` interface should make adding them straightforward, but they are not built now.
- **Postgres, MySQL, or other RDBMS stores.** The `store.Store` interface should make adding them straightforward, but they are not built now.
- **Linux or macOS hosts.** The host side is Windows only.
- **General remote shell (bash/zsh) or non-Windows PowerShell.** PowerShell on Windows only.
- **File transfer, port forwarding, GUI remote desktop.** Out of scope.
- **Multi-user shared sessions.** A session belongs to one user.
- **Auto-update of the mobile app.** That is the platform's responsibility (Play Store, App Store).
- **A web UI client.** Mobile only for the initial roadmap.

## What "v1" looks like

For the purposes of this document, v1 is the public release at the end of Phase 7. It includes:

- A working Android and iOS app available through Play Store / App Store / F-Droid.
- A working `peershd` Windows installer (MSI) with service registration.
- A working `peersh-signaling` binary with Docker image.
- Both deployment modes (official hosted and self-hosted) working end to end.
- All seven phases' content, including session resumption and background persistence.
- Documented protocol, documented self-hosting, documented Firebase setup, documented security posture.

Earlier phases ship usable artifacts (Phase 1 ships a CLI client; Phase 4 ships a working app), but they are not the public v1 release.
