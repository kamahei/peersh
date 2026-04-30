# Architecture

This file describes the technical shape of peersh: components, transport, NAT traversal, pluggable interfaces, protocol versioning, device identity, threat model, and the proposed repository layout. For product behavior see `product-spec.md`. For phase order see `implementation-plan.md`.

## High-level diagram

```
[Mobile App: Flutter UI + Go network layer (gomobile)]
       ↓ Auth (Firebase / PSK / None)
[Signaling Server: Go binary, deployable anywhere]
       ↓ endpoint exchange + (optionally) FCM wake-up
[Windows Service: Go + PowerShell host]
       ↑↓ UDP Hole Punching → QUIC P2P (mTLS)
[Mobile App]
```

The signaling server is **only used for connection setup**. All actual data flows P2P over QUIC. This is a load-bearing invariant: it keeps server costs near zero in the official hosted mode, gives strong privacy guarantees (server operator cannot see command content), and removes the entire class of bandwidth-cost concerns from the design.

## Components

### Mobile app

- **UI** in Flutter (Dart).
- **Network layer** in Go, compiled to mobile via `gomobile bind` (`.aar` for Android, `.xcframework` for iOS).
- Dart and Go communicate via Method Channel and EventChannel.
- Owns: pairing UX, server list, device list, terminal UI, secure storage of credentials, FCM token registration (when Firebase mode is enabled), background persistence (Android Foreground Service / iOS Background Modes).

#### Mobile architecture (Phase 4)

The mobile track is implemented as:

- `mobile-core/` — a Go package with a gomobile-friendly API surface. Phase 4a shipped `Version()` and `Echo()`. Phase 4b adds the `Session` lifecycle: `OpenDirectSession`, `OpenSignalingSession` (registration + STUN + Punch + sequential dial), `Exec(cmd, Output)` (streaming), `ReadFile(path)` (one-shot). `Output` is a gomobile callback interface (`OnStdout` / `OnStderr` / `OnDone`) implemented platform-side. Internally `mobile-core` reuses `core/transport`, `core/transport/devtls`, `core/wire`, `core/protocol/peersh/v1`, `core/punching`, and `core/signaling`.
- `scripts/build-mobile-core.{sh,cmd}` — wraps `gomobile bind` to produce `app/android/app/libs/peersh.aar` (Android, Windows or macOS host) and `app/ios/Frameworks/peersh.xcframework` (iOS, macOS host only).
- `app/` — Flutter project (`flutter create app --org dev.peersh`). Riverpod + flutter_secure_storage + http are pinned in `pubspec.yaml`.
- **MethodChannel `dev.peersh/bridge`** carries control-plane calls (`version`, `echo`, `openDirectSession`, `openSignalingSession`, `exec`, `readFile`, `closeSession`).
- **EventChannel `dev.peersh/session/events`** carries per-session output events as `{sessionId, type, data, error}`. Events from all sessions multiplex through the same channel; consumers filter by `sessionId`.
- Native bridges: `app/android/.../MainActivity.kt` (Kotlin, full implementation) and `app/ios/Runner/AppDelegate.swift` (Swift, code-complete pending the macOS xcframework build) keep a session map keyed by an incrementing `sessionId` and run blocking Go calls on a worker thread.
- Dart UI: `ServersScreen` → `ServerEditorScreen` (with discovery prefill via `/.well-known/peersh.json`) → `TerminalScreen` (with the wrap/scroll toggle, IME bottom sheet, and text viewer entry point) → `TextViewerScreen`. `SettingsScreen` carries the persisted line-wrap default + font size. Riverpod providers under `app/lib/state/` persist `ServerEntry` records and `AppSettings` via `flutter_secure_storage`.
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
- Owns: device registration, pairing/matching, endpoint exchange, optional FCM wake-up, rate limiting, auth verification, store-backed persistence.
- Configurable via TOML/YAML file plus environment variables.
- Uses WebSocket for the client-server signaling channel. WebSocket is used (rather than Firestore real-time listeners) so that the official hosted mode keeps Firestore costs bounded.
- Stateless across restarts to the extent possible: durable state lives in the configured store.

### Windows host (`peershd`)

- Single Go binary. Phase 1 ships as a console app; Phase 7 adds Windows Service registration.
- Owns: device keypair generation and persistence, signaling registration, NAT-punched UDP socket, QUIC server, PowerShell session host (`pwsh.exe -NoExit -Command -` with stdin/stdout/stderr piping), session manager (idle timeout, ring buffer, reattach), FCM wake-up listener (Phase 5+).

### CLI client (`peersh-cli`)

- Go binary. Used in Phase 1 for end-to-end testing before the mobile app exists, and useful afterwards as a developer/operator tool.

## Transport

- **QUIC over UDP** via `github.com/quic-go/quic-go`. QUIC mandates TLS 1.3, which gives end-to-end encryption for free.
- **mTLS.** Both ends authenticate to each other using their device keypairs. The same keypair that produces the device ID (see "Device identity") is used as the TLS credential.
- **External `net.PacketConn` requirement.** The QUIC wrapper in `core/transport/` must accept an externally-supplied `net.PacketConn`. This is non-negotiable: Phase 3 (NAT hole punching) requires reusing the punched UDP socket as the underlying transport for QUIC. A transport API that creates its own socket internally cannot be retrofitted for hole punching, and discovering this in Phase 3 would mean redoing Phase 1's transport code. Phase 1 includes a small test that exercises this path with `quic.Transport`.
- **Self-signed certs in development.** Phase 1 generates self-signed certs and the client uses `InsecureSkipVerify`, clearly marked as dev-only. Production-mode certificate handling is part of mTLS work in later phases.

## NAT traversal

- **UDP hole punching** is the only traversal mechanism. There is no relay/TURN fallback.
- **Endpoint discovery** uses `pion/stun` to learn reflexive addresses. Phase 3 ships this as `core/punching.Discover`. Default STUN server is `stun.l.google.com:19302`; both `peershd` and `peersh-cli` accept a `-stun` flag to override.
- **IPv6-first, IPv4-fallback.** `core/punching.SortCandidates` orders candidates SRFLX→HOST and IPv6→IPv4. The sequential dialer in `peersh-cli` tries them in that order with a 2 s timeout per candidate.
- **Reuse of the punched UDP socket.** STUN, punch packets, and QUIC all share one `*net.UDPConn`. STUN runs once at startup before `transport.New` takes over reads; subsequent `punching.Punch` calls write to the same socket while QUIC is alive (`net.PacketConn.WriteTo` is goroutine-safe). This is what the external `net.PacketConn` requirement on Phase 1 transport exists to enable.
- **Punch packet shape.** A 4-byte ASCII sentinel `pesh` plus a 12-byte random nonce — 16 bytes total. The first byte does not have QUIC's long-header bit set, so the peer's `quic-go` Transport drops these as non-QUIC. Each punch burst is 5 packets at 200 ms intervals (~1 s total).
- **Bounded retry policy.** Per-candidate dial timeout 2 s, single attempt each. With 4 candidates the worst-case budget is ~10 s including punch. On full failure the caller surfaces `punching.ErrTraversalFailed` ("Direct connection not possible from this network.").
- **CGNAT-both-sides** is the documented fail mode: symmetric NATs allocate a different external port per destination, so the srflx learned from STUN is wrong for the peer. `peersh-cli` exits cleanly with the actionable error; no relay path exists.
- **Optional birthday-paradox port scan** for symmetric NAT cases. Deferred per `docs/open-questions.md`.

## Pluggable authentication

The signaling server accepts a configurable auth provider via the `auth.Provider` interface. Three implementations are planned:

- **`none`** — no authentication. For development, LAN-only deployments, and the initial PoC. Lives at `core/auth/none/`.
- **`psk`** — pre-shared key. The server holds a list of `(user_id, secret_key)` pairs (initially in the configured store). Clients sign their registration request with HMAC-SHA256 over the payload + timestamp + nonce. Recommended for personal and small-group self-hosting. Lives at `core/auth/psk/`.
- **`firebase`** — verifies Firebase Auth ID tokens. Used by the official hosted server and any user who hosts their own Firebase project. Lives at `core/auth/firebase/`.

**Out of scope for the initial roadmap**: OIDC, OAuth2, generic SSO. The interface should make adding them straightforward later, but they are not part of the planned phases.

The `auth.Provider` interface lives in `core/auth/`. **Firebase types must not leak into `core/`**: when the firebase provider is built (Phase 5), its dependency on the Firebase SDK lives entirely under `core/auth/firebase/`, not in any shared package.

## Pluggable storage

The signaling server accepts a configurable store via the `store.Store` interface. Three implementations are planned:

- **In-memory** — for development, tests, and ephemeral signaling-only deployments. Lives at `core/store/memory/`.
- **SQLite** — recommended self-hosting default. No external DB to run. Lives at `core/store/sqlite/`.
- **Firestore** — for the official server and Firebase-mode users. Lives at `core/store/firestore/`.

The `store.Store` interface lives in `core/store/`. The same separation rule applies: Firebase/Firestore types must not leak into `core/`.

For domain entities, lifecycle, and per-backend mapping notes, see `data-model.md`.

## Device identity

Device IDs are **derived from the device's public key**:

```
device_id = base32(sha256(publicKey)[:16])  // 16-character ASCII ID
```

Implications:

- Reinstalling the app produces a new device ID. This is acceptable — treat reinstall as a new device.
- The same key serves both as identity and as the credential for mTLS. There is no separately stored device UUID.
- The trusted directory (Firestore in Firebase mode, SQLite in PSK mode) holds the binding from device ID to the user/account it belongs to. This is what prevents impersonation: a device cannot claim someone else's identity because its public key won't match their entries.

## Protocol versioning

The wire protocol carries a version from day one. Immediately after the QUIC handshake completes, the client opens a dedicated **control stream** (the first client-initiated bidirectional stream) and exchanges `ClientHello` / `ServerHello` once per connection. Subsequent application streams (e.g. per-command exec streams) skip the Hello; the negotiated capabilities apply to the whole connection. Both Hello messages contain:

- `protocol_version` (`uint32`, currently `1`)
- `capabilities` (`repeated string`, e.g. `["session_resume", "ipv6"]`)
- A free-form client/server identifier string

**Mismatched major versions must fail cleanly with an actionable error.** Capability strings allow optional features to be negotiated without bumping the version. Bumping `protocol_version` is the only way to make a breaking change.

This contract goes public the moment Phase 1 ships. Plan changes carefully.

## Wire formats

- **Protobuf** for all messages on the wire (`google.golang.org/protobuf`). The `proto/` directory is the single source of truth for message definitions; generated Go lives under `core/protocol/`.
- **WebSocket** carries protobuf-encoded signaling messages between clients and the signaling server. Each WebSocket binary frame is exactly one marshaled `peersh.signal.v1.Frame` (no length prefix; the WebSocket framing is the message boundary).
- **QUIC streams** carry length-prefixed protobuf application messages (`ClientHello`, `ServerHello`, `ExecRequest`, `ExecResponse`, more in later phases) between clients and `peershd`. Length prefix is a varint (see `core/wire`).

## Signaling protocol

The signaling channel runs on WebSocket and is connection-setup-only — it never carries command bytes. The server forwards `Connect` messages between paired devices and otherwise stays out of the data path.

Per-connection state machine:

1. **`ClientHello` → `ServerHello`** — version + capabilities negotiation. `protocol_version = 1` is locked.
2. **`Register` → `RegisterAck`** — PSK-signed identity assertion. The server verifies the HMAC-SHA256 signature against `core/auth/psk` and records (or refreshes) the device under the authenticated user_id.
3. **`Connect` (in either direction)** — the initiator sends `Connect{target_device_id, candidates}`; the server fills `from_device_id` (clients cannot spoof it) and forwards to the target if and only if the target is registered under the same user_id. Cross-user routing is rejected with a `ServerError` carrying `target_unknown` (the target is invisible to the sender's lookup) or `cross_user_forbidden`.
4. **`ServerError`** — anything that went wrong; close semantics depend on the code.

The full schema is in `proto/peersh/signal/v1/`. Implementation lives in `server/ws` (server side) and `core/signaling` (client library used by both `peershd` and `peersh-cli`).

## Threat model summary

- **Adversary types we care about**: a malicious or curious signaling server operator; a passive network observer; an attacker who has compromised one device and is trying to impersonate another.
- **Confidentiality.** The signaling server cannot read command contents. QUIC's TLS 1.3 protects in-flight traffic from any network observer.
- **Authentication.** Connections use mTLS with keypair-derived device identities. The trusted directory (the configured store) maps device IDs to users; an attacker without the matching private key cannot impersonate a device, even if they control the signaling server.
- **Out of model (initially).** Endpoint compromise (malware on a paired phone or PC). Side channels in QUIC implementations. Long-term key rotation strategy. These are tracked but not addressed in early phases.
- **`docs/security.md`** is intentionally deferred per the project plan ("as the project grows"). When the project gains real users, that file is created and this section is consolidated into it.

## Cost discipline (official hosted mode)

To keep the official hosted server within the Firebase/GCP free tier at low-thousands-of-users scale:

- Each connection lifecycle should consume **at most ~5 Firestore reads and ~2 writes**. Design Firestore access patterns and indexes against this budget.
- Use **client-side caching** for device info and public keys. Don't read what the client already knows.
- **No Firestore real-time listeners** for the signaling path. Signaling uses WebSocket with in-memory server-side state.
- Batch operations where possible.
- **Cost guardrails** at the project level (Phase 5+): Budget Alerts, App Engine Daily Spending Limit, auto-disable of the FCM Cloud Function when a budget breach occurs.

## Repository layout (proposed starting point)

This is a **starting proposal**, not a frozen layout. Phase 1 planning is the appropriate moment to refine it.

```
peersh/
├── core/                        # Shared Go packages
│   ├── protocol/                # Generated protobuf message code
│   ├── transport/               # QUIC wrapper around quic-go
│   ├── punching/                # UDP hole punching (Phase 3)
│   ├── signaling/               # Signaling client (Phase 2+)
│   ├── auth/                    # auth.Provider interface + implementations
│   │   ├── none/
│   │   ├── psk/                 # added in Phase 2
│   │   └── firebase/            # added in Phase 5
│   └── store/                   # store.Store interface + implementations
│       ├── memory/
│       ├── sqlite/              # added in Phase 2
│       └── firestore/           # added in Phase 5
├── windows/                     # Windows-side binary
│   ├── cmd/peershd/
│   ├── pwsh/                    # PowerShell session host
│   └── service/                 # Windows Service registration (Phase 7)
├── server/                      # Signaling server
│   ├── cmd/peersh-signaling/
│   ├── ws/                      # WebSocket handlers
│   ├── room/                    # pairing/matching logic
│   └── deploy/                  # Dockerfile, compose, etc.
├── mobile-core/                 # gomobile bind wrappers (Phase 4)
├── app/                         # Flutter project (Phase 4)
│   ├── android/
│   ├── ios/
│   └── lib/
├── proto/                       # .proto files (single source of truth)
├── docs/                        # Architecture, self-hosting, protocol docs
├── scripts/                     # Build, codegen, deployment helpers
├── LICENSE                      # Apache 2.0
└── README.md
```

The repository uses a Go workspace (`go.work`) to manage the multiple Go modules. Module boundaries follow the directory structure: `core/` is one module, `server/` another, `windows/` another, `mobile-core/` another. Exact module count and boundaries are decided in Phase 1 planning.

## Recurring architectural rules

- **No Firebase types in `core/`.** All Firebase/Firestore symbols live under `core/auth/firebase/` or `core/store/firestore/`. Importing the Firebase SDK from anywhere else is a layering violation.
- **No per-connection state in package globals.** The codebase must support hosting many concurrent connections. State that varies per connection is owned by structs you can construct and tear down, not by package-level variables.
- **Pluggable interfaces from day one, even when only the trivial implementation ships.** Phase 1 ships only `auth/none` and `store/memory`, but the interfaces are in their final shape and the wiring is interface-driven.
- **Protocol stability is a contract, not a goal.** The first time Phase 1 is shipped publicly, `protocol_version=1` is locked. Capability strings handle additive changes; only `protocol_version` bumps handle breaking changes.
