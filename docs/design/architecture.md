# Architecture

This file describes the technical shape of peersh: components, transport, NAT traversal, pluggable interfaces, protocol versioning, device identity, threat model, and the repository layout. For product behavior see `product-spec.md`.

## High-level diagram

```
[Mobile App: Flutter UI + Go network layer (gomobile)]
       ↓ Auth (Firebase / PSK / None)
[Signaling Server: Go binary, deployable anywhere]
       ↓ endpoint exchange (signaling WS opens only briefly per session in Firebase mode)
[Host (Windows/macOS): Go + PowerShell / login-shell]
       ↑ Firebase mode: Realtime Database SSE wake-listener (out of band, no Cloud Run cost)
       ↑↓ UDP Hole Punching → QUIC P2P (mTLS)
[Mobile App]
```

The signaling server is **only used for connection setup**. All actual data flows P2P over QUIC. This is a load-bearing invariant: it keeps server costs near zero in Firebase mode, gives strong privacy guarantees (server operator cannot see command content), and removes the entire class of bandwidth-cost concerns from the design.

## Components

### Mobile app

- **UI** in Flutter (Dart).
- **Network layer** in Go, compiled to mobile via `gomobile bind` (`.aar` for Android, `.xcframework` for iOS).
- Dart and Go communicate via Method Channel and EventChannel.
- Owns: pairing UX, server list, device list (read from Realtime Database in Firebase mode), terminal UI, secure storage of credentials, background persistence (Android Foreground Service / iOS Background Modes), and writing wake events to RTDB so the host can open its short-lived signaling WebSocket in response.

#### Mobile architecture

The mobile track is implemented as:

- `mobile-core/` — a Go package with a gomobile-friendly API surface: `OpenDirectSession`, `OpenSignalingSession` (PSK), `OpenFirebaseSignalingSession`, `Session.OpenPTY` / `OpenPTYReattach`, `Session.Exec`, `Session.ReadFile`, plus the `Output` and `PTYHandler` callback interfaces. Internally `mobile-core` reuses `core/transport`, `core/transport/peertls`, `core/wire`, `core/protocol/peersh/v1`, `core/punching`, and `core/signaling`.
- `scripts/build-mobile-core.{sh,cmd}` — wraps `gomobile bind` to produce `app/android/app/libs/peersh.aar` and `app/ios/Frameworks/peersh.xcframework` (iOS host on macOS only).
- `app/` — Flutter project (`flutter create app --org dev.peersh`). Riverpod + flutter_secure_storage + http + xterm + firebase plugins are pinned in `pubspec.yaml`.
- **MethodChannel `dev.peersh/bridge`** carries control-plane calls (session lifecycle, PTY lifecycle, file API, foreground-service start/stop).
- **EventChannel `dev.peersh/session/events`** carries per-session and per-PTY events tagged with `sessionId` / `ptyId`.
- Native bridges: `app/android/.../MainActivity.kt` (Kotlin) keeps the session and PTY maps; `PeershForegroundService.kt` holds the OS off the app process while connected. The iOS bridge is the shared Swift file `app/shared/apple/PeershBridge.swift`, wired into `app/ios/Runner/AppDelegate.swift` — the same MethodChannel/EventChannel surface, ported from the Kotlin reference.
- Dart UI: `ServersScreen` → optional `DevicePickerSheet` (Firebase entries) → `TerminalTabsScreen` (multi-tab xterm with reattach + special-keys bar) → `FileBrowserScreen` / `TextViewerScreen`. Auto-reconnect with exponential backoff sits in `TerminalTabsScreen`; "Session resumed" banner fades after 4 s on a successful reconnect.
- The AAR / xcframework are not committed; each developer or CI rebuilds them via the build script.

#### Discovery: `/.well-known/peersh.json`

Served at the signaling server's HTTPS root. The mobile app fetches it when the user adds a server by hostname only:

```json
{
  "version": 1,
  "ws_url": "wss://signaling.example.com/ws",
  "stun_servers": ["stun.l.google.com:19302"],
  "auth_providers": ["psk"]
}
```

Operators populate `[discovery]` in `signaling.toml` (see `server/deploy/signaling.example.toml`). The endpoint is GET / HEAD; everything else gets 405. Cache-Control: no-cache so configuration changes propagate quickly.

### Signaling server (`peersh-signaling`)

- Single Go binary. Deployable as a binary or Docker container.
- Owns: device registration, pairing/matching, endpoint exchange, rate limiting, auth verification, store-backed persistence, idle-close defense layer.
- Configurable via TOML/YAML file plus environment variables.
- Uses WebSocket for the client-server signaling channel. In Firebase mode the WS is short-lived (opened in response to a Realtime Database wake event, closed after the session is established); idle close defaults to 60 s. In PSK mode the host holds it open continuously.
- Stateless across restarts to the extent possible: durable state lives in the configured store.

### Host (`peershd` — Windows / macOS)

- Single Go binary running on Windows or macOS (the whole `windows/` module compiles for both; only the PTY backend and shell resolver are platform-tagged). Runs as a console app or an auto-start service: a Windows Service / logon task (`peershd -install` / `-install-logon-task`) or a macOS per-user LaunchAgent (`peershd -install`; see `scripts/install-peershd-macos.sh`).
- Owns: device keypair generation and persistence, signaling registration, NAT-punched UDP socket, QUIC server, a real PTY host (ConPTY + PowerShell on Windows, `forkpty` + the user's login shell — zsh/bash — on macOS), session manager (idle timeout, ring buffer, reattach), Realtime Database wake listener (Firebase mode — replaces the persistent signaling WebSocket; works for both pair-code and service-account credentials via the SSE REST API and Firebase ID tokens), self-update subcommand. The legacy one-shot `exec.v1` PowerShell path is Windows-only; interactive PTY streams work on both.

### CLI client (`peersh-cli`)

- Go binary. Useful for end-to-end testing without the mobile app, and as a daily PowerShell client on the operator's PC.
- `-pty` enters interactive PTY mode. By default it lists the host's persisted PTYs (created from any of the operator's devices — mobile, this CLI, another PC) and prompts the user to attach to one or create a new shell. Multi-attach: picking a PTY that another device is already observing simply joins it; the host fans output out to every attached client and merges input from all of them.
- Flags: `-pty-new` skips the picker and spawns a fresh shell; `-pty-reattach <handle>` jumps directly to a known handle; `-pty-list` prints the list and exits (script-friendly).

## Transport

- **QUIC over UDP** via `github.com/quic-go/quic-go`. QUIC mandates TLS 1.3, which gives end-to-end encryption for free.
- **mTLS bound to device IDs.** Both ends authenticate to each other using an ed25519 keypair — the same keypair that produces the device ID (see "Device identity"). Production code uses `core/transport/peertls`: each peer presents a self-signed cert whose private key is its device key, the server requires a client cert (`tls.RequireAnyClientCert`), and the client side pins the server's expected `device_id` so a host that presents a different key fails the TLS handshake.
- **External `net.PacketConn` requirement.** The QUIC wrapper in `core/transport/` accepts an externally-supplied `net.PacketConn` so the punched UDP socket can be reused as the underlying transport for QUIC. A regression test (`TestExternalPacketConnContract`) exercises the path with `quic.Transport`.
- **`devtls` is now test-only.** `core/transport/devtls` still generates self-signed certs and a client config with `InsecureSkipVerify`, marked as dev-only via the grep-able `DevSelfSignedOnly` constant. Production code paths no longer import it; new code should use `peertls` instead.

## NAT traversal

- **UDP hole punching** is the only traversal mechanism. There is no relay/TURN fallback.
- **Endpoint discovery** uses `pion/stun` to learn reflexive addresses through `core/punching.Discover`. Default STUN server is `stun.l.google.com:19302`; both `peershd` and `peersh-cli` accept a `-stun` flag to override.
- **IPv6-first, IPv4-fallback.** `core/punching.SortCandidates` orders candidates SRFLX→HOST and IPv6→IPv4. The sequential dialer tries them in that order with a 2 s timeout per candidate.
- **Reuse of the punched UDP socket.** STUN, punch packets, and QUIC all share one `*net.UDPConn`. STUN runs once at startup before `transport.New` takes over reads; subsequent `punching.Punch` calls write to the same socket while QUIC is alive (`net.PacketConn.WriteTo` is goroutine-safe).
- **Punch packet shape.** A 4-byte ASCII sentinel `pesh` plus a 12-byte random nonce — 16 bytes total. The first byte does not have QUIC's long-header bit set, so the peer's `quic-go` Transport drops these as non-QUIC. Each punch burst is 5 packets at 200 ms intervals (~1 s total).
- **Bounded retry policy.** Per-candidate dial timeout 2 s, single attempt each. With 4 candidates the worst-case budget is ~10 s including punch. On full failure the caller surfaces `punching.ErrTraversalFailed` ("Direct connection not possible from this network.").
- **CGNAT-both-sides** is the documented fail mode: symmetric NATs allocate a different external port per destination, so the srflx learned from STUN is wrong for the peer. `peersh-cli` exits cleanly with the actionable error; no relay path exists.

## Pluggable authentication

The signaling server accepts a configurable auth provider via the `auth.Provider` interface. Three implementations ship:

- **`none`** — no authentication. For development, LAN-only deployments, and the same-LAN PoC. Lives at `core/auth/none/`.
- **`psk`** — pre-shared key. The server holds a list of `(user_id, secret_key)` pairs in the configured store. Clients sign their registration request with HMAC-SHA256 over the payload + timestamp + nonce. Recommended for personal and small-group self-hosting. Lives at `core/auth/psk/`.
- **`firebase`** — verifies Firebase Auth ID tokens (and, optionally, App Check tokens). Used by Firebase-mode deployments. Lives at `core/auth/firebase/`.

OIDC, OAuth2, and generic SSO are out of scope today; the interface accommodates adding them.

The `auth.Provider` interface lives in `core/auth/`. **Firebase types must not leak into `core/`**: the Firebase SDK dependency lives entirely under `core/auth/firebase/`.

## Pluggable storage

The signaling server accepts a configurable store via the `store.Store` interface. Three implementations ship:

- **In-memory** — for development, tests, and ephemeral signaling-only deployments. Lives at `core/store/memory/`.
- **SQLite** — recommended PSK self-host default. No external DB to run. Lives at `core/store/sqlite/`.
- **Firestore** — for Firebase-mode deployments. Lives at `core/store/firestore/`.

The `store.Store` interface lives in `core/store/`. Firebase/Firestore types must not leak into `core/`.

For domain entities, lifecycle, and per-backend mapping notes, see `data-model.md`.

## Device identity

Device IDs are **derived from the device's public key**:

```
device_id = base32(sha256(publicKey)[:10])  // 16-character ASCII ID
```

Implications:

- Reinstalling the app produces a new device ID. This is acceptable — treat reinstall as a new device.
- The same key serves both as identity and as the credential for mTLS. There is no separately stored device UUID.
- The trusted directory (Firestore in Firebase mode, SQLite in PSK mode) holds the binding from device ID to the user/account it belongs to. This is what prevents impersonation: a device cannot claim someone else's identity because its public key won't match their entries.

## Protocol versioning

The wire protocol carries a version from day one. Immediately after the QUIC handshake completes, the client opens a dedicated **control stream** (the first client-initiated bidirectional stream) and exchanges `ClientHello` / `ServerHello` once per connection. Subsequent application streams (e.g. per-command exec streams) skip the Hello; the negotiated capabilities apply to the whole connection. Both Hello messages contain:

- `protocol_version` (`uint32`, currently `2` for the QUIC application protocol)
- `capabilities` (`repeated string`, e.g. `["session_resume", "ipv6"]`)
- A free-form client/server identifier string

**Mismatched versions must fail cleanly with an actionable error.** Capability strings allow optional features to be negotiated without bumping the version. Bumping `protocol_version` is the only way to make a breaking change.

## Wire formats

- **Protobuf** for all messages on the wire (`google.golang.org/protobuf`). The `proto/` directory is the single source of truth for message definitions; generated Go lives under `core/protocol/`.
- **WebSocket** carries protobuf-encoded signaling messages between clients and the signaling server. Each WebSocket binary frame is exactly one marshaled `peersh.signal.v1.Frame` (no length prefix; the WebSocket framing is the message boundary).
- **QUIC streams** carry length-prefixed protobuf application messages (`ClientHello`, `ServerHello`, `ExecRequest`, `ExecResponse`, more in later phases) between clients and `peershd`. Length prefix is a varint (see `core/wire`).

## Signaling protocol

The signaling channel runs on WebSocket and is connection-setup-only — it never carries command bytes. The server forwards `Connect` messages between paired devices and otherwise stays out of the data path.

Per-connection state machine:

1. **`ClientHello` → `ServerHello`** — version + capabilities negotiation. The signaling channel has its own `protocol_version = 1`, separate from the QUIC application protocol version.
2. **`Register` → `RegisterAck`** — PSK-signed identity assertion. The server verifies the HMAC-SHA256 signature against `core/auth/psk` and records (or refreshes) the device under the authenticated user_id.
3. **`Connect` (in either direction)** — the initiator sends `Connect{target_device_id, candidates}`; the server fills `from_device_id` (clients cannot spoof it) and forwards to the target if and only if the target is registered under the same user_id. Cross-user routing is rejected with a `ServerError` carrying `target_unknown` (the target is invisible to the sender's lookup) or `cross_user_forbidden`.
4. **`ServerError`** — anything that went wrong; close semantics depend on the code.

The full schema is in `proto/peersh/signal/v1/`. Implementation lives in `server/ws` (server side) and `core/signaling` (client library used by both `peershd` and `peersh-cli`).

## Threat model summary

- **Adversary types we care about**: a passive network observer; a curious signaling server operator; an attacker who has compromised one device and is trying to impersonate another.
- **Confidentiality.** The signaling server cannot read command contents. QUIC's TLS 1.3 protects in-flight traffic from any network observer.
- **Authentication.** Connections use mTLS with keypair-derived device identities. The trusted directory (the configured store) maps device IDs to users and prevents a client from registering someone else's `device_id`. Connection authorization is still delegated to signaling: `peershd` accepts a QUIC peer whose authenticated `device_id` matches the recent `Connect.from_device_id` grant. A hostile shared signaling deployment requires a future host-side client allowlist; self-hosted deployments assume the signaling operator and host operator are the same trust domain.
- **Out of model (initially).** Endpoint compromise (malware on a paired phone or PC). Side channels in QUIC implementations. Long-term key rotation strategy. These are tracked but not addressed in early phases.
- **A dedicated `docs/security.md`** is deferred until real-user scale demands it; for now the threat-model summary lives here and security disclosure is in `SECURITY.md`.

## Cost discipline (Firebase mode)

To keep a Firebase-mode signaling server within the Firebase / GCP free tier at low-thousands-of-users scale:

- Each connection lifecycle should consume **at most ~5 Firestore reads and ~2 writes**. Design Firestore access patterns and indexes against this budget.
- Use **client-side caching** for device info and public keys. Don't read what the client already knows.
- **No real-time listeners on signaling messages** (Hello/Register/Connect). The host-side Realtime Database SSE listener on `users/{uid}/wake_requests` is an explicit cost-discipline exception, sanctioned because it replaces the persistent signaling WebSocket and the connection goes to `firebasedatabase.app` (not Cloud Run).
- Batch operations where possible.
- **Cost guardrails** at the project level: Cloud Billing budgets and the App Check enforcement switch on Register frames. The `budgetGuard` Cloud Function and `ops/budget-state` short-circuit are retained for a future `onSessionCreated` revival; at present neither is on the wake-event path.

## Repository layout

```
peersh/
├── core/                        # Shared Go packages
│   ├── protocol/                # Generated protobuf message code
│   ├── transport/               # QUIC wrapper around quic-go
│   ├── punching/                # UDP hole punching
│   ├── signaling/               # Signaling client
│   ├── auth/                    # auth.Provider interface + implementations
│   │   ├── none/
│   │   ├── psk/
│   │   └── firebase/
│   └── store/                   # store.Store interface + implementations
│       ├── memory/
│       ├── sqlite/
│       └── firestore/
├── windows/                     # host binary (Windows + macOS; dir name is historical)
│   ├── cmd/peershd/             # peershd entry point + service / update
│   ├── pwsh/                    # legacy PowerShell exec host (Windows-only)
│   ├── pty/                     # PTY backend: ConPTY (Windows) / forkpty (macOS)
│   ├── ptyhost/                 # PTY persistence + ring buffer
│   ├── firebase/                # Browser sign-in / pairing / refresh-token
│   └── installer/               # WiX MSI definition
├── server/                      # Signaling server
│   ├── cmd/peersh-signaling/
│   ├── ws/                      # WebSocket handler
│   ├── room/                    # pairing / matching logic
│   ├── ratelimit/               # token-bucket rate limiter
│   ├── admin/                   # admin endpoints (PSK CRUD)
│   └── deploy/                  # Dockerfile, Cloud Run / Render / Fly templates
├── cli/cmd/peersh-cli/          # peersh-cli REPL client
├── mobile-core/                 # gomobile bind wrappers (Go API)
├── app/                         # Flutter project
│   ├── android/
│   ├── ios/
│   └── lib/
├── firebase/                    # Firebase project artefacts (rules, functions)
├── proto/                       # .proto files (single source of truth)
├── docs/                        # Design + deploy + user manual
├── scripts/                     # Build, codegen, deployment helpers
├── LICENSE                      # Apache 2.0
└── README.md
```

The repository uses a Go workspace (`go.work`) to manage the multiple Go modules: `core/`, `server/`, `windows/`, `mobile-core/`, `cli/`.

## Recurring architectural rules

- **No Firebase types in `core/`.** All Firebase/Firestore symbols live under `core/auth/firebase/` or `core/store/firestore/`. Importing the Firebase SDK from anywhere else is a layering violation.
- **No per-connection state in package globals.** The codebase must support hosting many concurrent connections. State that varies per connection is owned by structs you can construct and tear down, not by package-level variables.
- **Pluggable interfaces in their final shape.** `auth.Provider` and `store.Store` are public surface; adding a new provider/store means implementing the interface, not editing `core/`.
- **Protocol stability is a contract.** Once a `protocol_version` is shipped, breaking changes require a version bump. Capability strings on Hello messages handle additive changes.
