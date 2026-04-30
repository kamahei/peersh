# peersh

Run PowerShell on your home Windows PC from your phone, peer-to-peer, without a VPN and without a relay server.

## What it is

`peersh` is a tool for executing PowerShell commands on a home Windows PC from a mobile device (Android / iOS) over the public internet. The data path is **direct peer-to-peer** over QUIC with mTLS — your commands and their output never travel through a third-party server. A small signaling server is used **only for connection setup**: it helps the two endpoints find each other, then steps out of the way.

The project is open source under **Apache License 2.0**.

## Two ways to use it

- **Official hosted signaling.** Sign in with Google. The official signaling server, backed by Firebase, handles the rendezvous. Designed to fit comfortably in the Firebase free tier at low-thousands-of-users scale.
- **Self-hosted signaling.** Run the `peersh-signaling` binary (or its Docker image) on a small VPS or your home server. No Firebase account needed. Use HMAC-based pre-shared keys for authentication, with a SQLite store for state. The bar is "less than five minutes from `docker run` to a working setup."

The mobile app supports adding multiple signaling servers and switching between them. You can use the official hosted server and a self-hosted one side by side.

## Status

**Phase 1 (Same-LAN PoC) is complete.** A Windows host (`peershd`) and a CLI client (`peersh-cli`) can talk to each other over QUIC on the same LAN, exchanging Hello messages and running real PowerShell commands. No auth, no signaling, no NAT — direct IP only. See "Build and verify Phase 1" below.

The seven planned phases are:

1. Same-LAN PoC (Go service + CLI client over LAN) — **done**
2. Signaling server with PSK auth (self-host path)
3. NAT hole punching (P2P across home networks / mobile data)
4. Flutter mobile app + `gomobile` integration
5. Firebase Auth + FCM wake-up (official hosted path)
6. Background persistence + session resumption
7. Polish, public release, and beyond

Each phase is a separate work session. AI agents working in this repository default to Plan Mode at the start of each phase — see `AGENTS.md` and `docs/ai-implementation-guide.md`.

## Build and verify Phase 1

### Prerequisites

- **Go 1.22+** on PATH.
- **`buf` and `protoc-gen-go`** for regenerating protobuf code (only needed if you change `.proto` files):
  ```sh
  go install github.com/bufbuild/buf/cmd/buf@latest
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  ```
- **Windows 10 / 11** with PowerShell 7 (`pwsh.exe`) preferred. Falls back to `powershell.exe` if `pwsh` is not on PATH.

### Regenerate protobuf code (only if you edited `proto/`)

```sh
scripts/gen-proto.sh        # macOS / Linux / Git Bash
scripts\gen-proto.cmd       # Windows cmd / PowerShell
```

Generated Go lands in `core/protocol/peersh/v1/` and is committed to the repository. You only need to regenerate after editing `.proto` files.

### Build

```sh
go build -o bin/peershd.exe ./windows/cmd/peershd
go build -o bin/peersh-cli.exe ./cli/cmd/peersh-cli
```

### Run the same-LAN demo

In one terminal (Windows host):

```sh
./bin/peershd.exe -listen :7777
```

`peershd` generates a self-signed dev cert under `%LOCALAPPDATA%\peersh\dev\` on first run (override with `-cert-dir`). Subsequent runs reuse it.

In another terminal (any machine that can reach the host on UDP 7777):

```sh
./bin/peersh-cli.exe -addr <host-ip>:7777
peersh> Get-Process | Select-Object -First 5 | Out-String
```

You should see a real `Get-Process` table streamed back. Type `exit` or send EOF to quit.

> The Phase 1 client uses `InsecureSkipVerify` against the self-signed cert. The grep-able constant `transport/devtls.DevSelfSignedOnly = true` makes that obvious in any audit. Real certificate verification arrives once signaling makes mutual auth meaningful (Phase 2+).

### Run tests

```sh
go test ./core/...                # interfaces, framing, transport contract
go test ./windows/...             # PowerShell session host (requires pwsh or powershell on PATH)
```

The load-bearing test for Phase 3 forward-compatibility is `TestExternalPacketConnContract` in `core/transport/`: it constructs `*net.UDPConn` for both server and client outside the transport package and runs a full QUIC stream through them. Phase 3's hole-punched socket will reuse this contract.

## Tech direction

- **Backend (host, signaling, mobile network layer):** Go.
- **Mobile UI:** Flutter (Dart). The Go network layer is shared across all clients via `gomobile bind` (`.aar` for Android, `.xcframework` for iOS), called from Dart through Method Channel / EventChannel.
- **Transport:** QUIC over UDP via `github.com/quic-go/quic-go`. TLS 1.3 mandatory.
- **NAT traversal:** UDP hole punching with `pion/stun`. **No relay/TURN.** When traversal cannot succeed (e.g. CGNAT on both sides), peersh fails with an actionable error rather than relaying.
- **Wire format:** Protobuf (`google.golang.org/protobuf`).
- **Pluggable auth:** `none` / `psk` / `firebase` behind `auth.Provider` (in `core/auth/`).
- **Pluggable storage:** in-memory / SQLite / Firestore behind `store.Store` (in `core/store/`).
- **Mobile UI integration:** FlutterFire when Firebase auth is selected.

## Documentation map

Project documentation lives under `docs/`:

- `docs/project-overview.md` — vision, users, goals, non-goals, design principles, environment.
- `docs/product-spec.md` — capabilities, user journeys, NFRs, explicit out-of-scope.
- `docs/architecture.md` — components, transport, NAT strategy, pluggable interfaces, protocol versioning, repository layout.
- `docs/data-model.md` — durable entities (Device, User, Pairing, Session, PSKRecord) and per-backend mappings.
- `docs/implementation-plan.md` — the seven-phase roadmap.
- `docs/task-breakdown.md` — Phase 1 broken down into bounded tasks; later phases referenced.
- `docs/acceptance-criteria.md` — cross-phase invariants and per-phase done criteria.
- `docs/open-questions.md` — what is not yet decided, with current default assumptions.
- `docs/ai-implementation-guide.md` — operating manual for AI agents working in this repo.
- `docs/glossary.md` — terms used across the docs and code.

For AI agents specifically, see `AGENTS.md` at the repository root for the short-form working instructions.

## Environment

- **Dev machine.** Windows 10 / 11 with JetBrains Rider or VS Code.
- **Target host.** Windows 10 / 11. PowerShell 7 (`pwsh`) preferred; falls back to `powershell.exe` if `pwsh` is not on PATH.
- **Go.** 1.22 or later.
- **Flutter.** Latest stable (used from Phase 4 onward).
- **Test topology.** Same-LAN testing for Phase 1. Multiple-network testing from Phase 2. Real NAT traversal from Phase 3.

## License

Apache License 2.0. See `LICENSE`.
