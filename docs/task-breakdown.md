# Task Breakdown

This file decomposes phase-level scope into bounded, independently implementable tasks. Phase 1 is broken down in full because it is the next phase up. Phases 2-7 are referenced at the section level — each gets its own breakdown when its turn arrives.

For phase definitions, scope, and validation, see `implementation-plan.md`.

## Working rules

- Tasks within a phase have explicit dependencies. Do not start a task before its prerequisites land.
- Each task has a **scope** (what it adds), **prerequisites** (what must already exist), and a **validation target** (how you know it's done).
- A task should be small enough that its diff fits in one reviewable change. If a task feels like it covers two things, split it.
- Adding a task or reordering tasks during implementation is fine as long as it is reflected back into this document.

## Phase 1 — Same-LAN PoC

The goal is the smallest end-to-end thing that works: `peersh-cli` on one machine, `peershd` on another machine on the same LAN, executing PowerShell commands over QUIC. No auth, no signaling, no NAT, no Service registration.

> **Status: shipped.** Phase 1 landed a 3-module Go workspace (`core/`, `windows/`, `cli/`), the protobuf wire types, the `auth.Provider` and `store.Store` interfaces with their `none` and `memory` implementations, the QUIC transport with the externally-supplied `net.PacketConn` contract, the PowerShell session host with sentinel-based completion detection, the `peershd` and `peersh-cli` binaries, and the README setup steps. The 3-module layout decision was confirmed with the user during Phase 1 planning; the brief's earlier "where peersh-cli lives — TBD" is resolved as `cli/cmd/peersh-cli/`.

### P1-T01: Repository skeleton and `go.work`

- **Scope.** Create three Go modules — `core/` (library), `windows/` (Windows-specific, holding `peershd` and the PowerShell host), `cli/` (cross-platform CLI). Add `proto/` for `.proto` source (not a Go module) and `scripts/` for codegen helpers. Tie the modules with `go.work`. Stub `doc.go` package files where needed to make the workspace build.
- **Prerequisites.** None.
- **Validation.** `go work sync` succeeds. `go build ./core/... ./windows/... ./cli/...` succeeds at the skeleton level.

### P1-T02: Protobuf definitions for Phase 1 messages

- **Scope.** Define `.proto` files under `proto/` for: `ClientHello`, `ServerHello`, `ExecRequest`, `ExecResponse`. Include `protocol_version` (uint32), `capabilities` (repeated string), and a free-form identifier in both Hello messages. Set up `scripts/` codegen (e.g. `buf` or vanilla `protoc-gen-go`) to emit Go under `core/protocol/`. Document the regen command in the README.
- **Prerequisites.** P1-T01.
- **Validation.** Codegen runs cleanly. Generated Go compiles. A trivial round-trip test marshals/unmarshals each message type.

### P1-T03: `auth.Provider` interface + `none` implementation

- **Scope.** Define the `auth.Provider` interface at `core/auth/`. Implement `core/auth/none/` as the trivial accept-everything provider. Keep the interface focused: it should support what Phase 1 needs (none) and what Phase 2 will need (PSK signature verification) without baking in Firebase concepts.
- **Prerequisites.** P1-T01.
- **Validation.** Interface compiles. `none` provider passes a small unit test that exercises its methods.

### P1-T04: `store.Store` interface + `memory` implementation

- **Scope.** Define the `store.Store` interface at `core/store/`. Implement `core/store/memory/` using mutex-protected maps. Cover the entities Phase 1 actually touches (Devices and Sessions; PSKRecord and persistent Pairings come in Phase 2). Tests for concurrent access.
- **Prerequisites.** P1-T01.
- **Validation.** Interface compiles. `memory` provider passes a small unit test for create/get/list/delete on the entities Phase 1 uses.

### P1-T05: `core/transport/` QUIC wrapper with external `net.PacketConn` support

- **Scope.** Implement a minimal QUIC server and client in `core/transport/`. The API **must accept an externally-supplied `net.PacketConn`** as the underlying transport — this is what unlocks Phase 3's hole-punched-socket reuse. Generate self-signed certs for development. Mark `InsecureSkipVerify` on the client side as dev-only (named constant or build tag).
- **Prerequisites.** P1-T02.
- **Validation.** A unit/integration test that spins up the server with a `*net.UDPConn` you created yourself, has the client connect using its own externally-supplied `net.PacketConn`, exchanges a `ClientHello`/`ServerHello`, and tears down cleanly. This test is the contract for the Phase 3 design.

### P1-T06: `windows/pwsh/` PowerShell session host

- **Scope.** A Go package that spawns `pwsh.exe -NoExit -Command -` (with `powershell.exe` fallback when `pwsh` is not on PATH) and pipes stdin/stdout/stderr. Provide a struct that wraps the process, a `Write(stdin)` method, and a way to consume stdout/stderr as a stream. Handle process exit cleanly. No session-resumption / ring-buffer logic yet — that is Phase 6.
- **Prerequisites.** P1-T01.
- **Validation.** A small standalone program (or test) starts the session host, sends `Get-Process | Select-Object -First 5`, and prints the streamed output until the command completes.

### P1-T07: `cmd/peershd` console entry point

- **Scope.** A console binary at `windows/cmd/peershd/` that wires together: `core/transport/` (QUIC server), `core/auth/none/`, `core/store/memory/`, `windows/pwsh/` (session host). On a new QUIC stream, perform the Hello handshake, then accept `ExecRequest` messages and forward them to the PowerShell session host, streaming output back as `ExecResponse` messages. Flag-based config (port, optional cert path).
- **Prerequisites.** P1-T03, P1-T04, P1-T05, P1-T06.
- **Validation.** Build the binary. Run it on a Windows machine. Confirm it logs that it's listening.

### P1-T08: `cli/cmd/peersh-cli` REPL client

- **Scope.** A simple REPL CLI binary at `cli/cmd/peersh-cli/`, in the cross-platform `cli/` module. Connects to a `peershd` host given an address. Performs the Hello exchange on QUIC stream 0. Reads lines from stdin, sends as `ExecRequest` on a fresh stream per command, prints streamed `ExecResponse` output until `done = true`, then prompts again. Handles disconnect with a clean error message.
- **Prerequisites.** P1-T05, P1-T07.
- **Validation.** Run `peershd` and `peersh-cli`, execute several commands interactively, confirm `Get-Process` style output streams correctly.

### P1-T09: Self-signed cert generation

- **Scope.** A small helper (in `scripts/` or `core/transport/`) that generates a self-signed cert and key on first run and stores them under a known path. Used by `peershd` and the dev workflow. Clearly labeled dev-only.
- **Prerequisites.** P1-T05.
- **Validation.** Running `peershd` for the first time generates the cert and starts cleanly; subsequent runs reuse it.

### P1-T10: README setup and verification steps

- **Scope.** Update `README.md` with: how to install the toolchain (Go, protobuf compiler), how to run codegen, how to build `peershd` and `peersh-cli`, how to run the same-LAN verification end to end, and how to run the `core/transport/` external-`PacketConn` test.
- **Prerequisites.** P1-T07, P1-T08.
- **Validation.** A reader following the README on a clean machine reaches a working same-LAN demo.

### P1 done when

- All Phase 1 acceptance criteria in `acceptance-criteria.md` pass.
- Two machines on the same LAN run a real PowerShell session end to end via `peershd` ↔ `peersh-cli`.
- The external-`net.PacketConn` test demonstrates the contract that Phase 3 depends on.

## Phase 2 — Signaling Server with PSK Auth

> **Status: shipped.** Phase 2 landed the `server/` Go module (`peersh-signaling` binary with `serve` + `psk {add,list,revoke}` subcommands), the `core/auth/psk` HMAC-SHA256 provider with replay protection, the `core/store/sqlite` modernc-backed store with embedded migrations, the `core/signaling` WebSocket client, the protobuf-binary signaling channel under `proto/peersh/signal/v1/`, per-IP / per-user / per-device rate limiting, TOML + env-var configuration, and Dockerfile + docker-compose.yml + `docs/self-hosting.md` for self-hosting. Pairing is **implicit under shared PSK user_id**; explicit token / QR pairing arrives with the mobile app in Phase 4.

The bounded tasks Phase 2 was decomposed into were P2-T01 through P2-T18:

- T01: server/ module skeleton.
- T02: signaling protobuf (`peersh.signal.v1`).
- T03: core/store interface extensions (User, PSKRecord, Pairing).
- T04: core/store/sqlite (modernc.org/sqlite).
- T05: core/auth/psk + nonce cache + Sign/Verify helpers.
- T06: core/signaling client library.
- T07: server/ws upgrade + Hello/Register state machine.
- T08: server/room registry + Connect routing.
- T09: server/ratelimit token bucket.
- T10: server/config TOML loader + env overrides.
- T11: server/admin PSK CRUD + `psk` subcommands.
- T12: server/cmd/peersh-signaling main binary with `serve`.
- T13: peershd signaling integration.
- T14: peersh-cli signaling integration.
- T15: end-to-end integration test.
- T16: Dockerfile + docker-compose.yml + signaling.example.toml.
- T17: docs/self-hosting.md.
- T18: doc reconciliation.

## Phase 3 — NAT Hole Punching

> **Status: shipped.** Phase 3 landed `core/punching` (STUN `Discover`, magic-byte `Punch`, IPv6-first `SortCandidates`, `ErrTraversalFailed` user-facing message), wired it into `peershd` (STUN once at startup, SRFLX in candidates, Punch on every incoming Connect) and `peersh-cli` (STUN once at startup, SRFLX in candidates, Punch + sequential preferred-order dial), and added an in-process loopback end-to-end test (`server/ws/phase3_e2e_test.go`) covering STUN → signaling → Punch → QUIC dial → Hello + Exec round-trip. The `core/transport` external-`PacketConn` contract is exercised here for the first time outside its test suite.

Phase 3 was decomposed into P3-T01 through P3-T06:

- T01: `core/punching` package skeleton + `pion/stun/v2` dep + `Discover` + tests against an in-process STUN responder.
- T02: `Punch` + `SortCandidates` + `CandidatesToUDPAddrs` + tests.
- T03: `peershd` integration (`-stun` flag, Discover at startup, SRFLX in `enumerateCandidates`, Punch on incoming Connect).
- T04: `peersh-cli` integration (`-stun` flag, Discover before signaling, SRFLX in `localCandidates`, Punch + sequential dial loop with `ErrTraversalFailed`).
- T05: `server/ws/phase3_e2e_test.go` end-to-end loopback test.
- T06: doc reconciliation (architecture NAT-traversal section, open-questions Phase 3 resolved, task-breakdown shipped, README status, self-hosting one-line note).

## Phase 4 — Flutter App + gomobile Integration

Phase 4 is split across two sessions. Phase 4a (the spike + foundation) is shipped; Phase 4b (real UI screens, multi-server, secure storage, iOS device validation) is the next session.

### Phase 4a — Spike + Foundation

> **Status: shipped (Android toolchain validated; real-device test pending).** Phase 4a landed `mobile-core/` with a gomobile-friendly bridge API (`Version` + `Echo`), `scripts/build-mobile-core.{sh,cmd}` wrapping `gomobile bind`, the `app/` Flutter project with Riverpod and a verification spike screen, the `dev.peersh/bridge` MethodChannel handlers on Android (Kotlin, working) and iOS (Swift, code-complete pending macOS build), and the `/.well-known/peersh.json` discovery endpoint on `peersh-signaling`. The Android `.aar` builds (4 ABIs, 28 MB) and `flutter build apk --debug` succeeds (116 MB debug APK). Real device run is the user's spike step.

Decomposed into P4a-T01 through P4a-T08:

- T01: gomobile install + Android NDK 26.x via sdkmanager + `gomobile init`.
- T02: `mobile-core/` Go module — `Version`, `Echo` reusing core/transport.
- T03: `gomobile bind -target=android` → `peersh.aar`. (Spike pass/fail signal: PASS.)
- T04: `flutter create app --org dev.peersh`, Riverpod added, `flutter analyze` clean.
- T05: bridge wiring — `bridge.dart`, `MainActivity.kt`, `AppDelegate.swift` (deferred), `build.gradle` flatDir AAR dep, spike screen.
- T06: `flutter build apk --debug` — full toolchain compile.
- T07: `/.well-known/peersh.json` discovery endpoint on `peersh-signaling` + tests + config wiring.
- T08: doc reconciliation.

### Phase 4b — Real UI + iOS Device

To start the next session: real UI screens (pairing, server list, device list, terminal/session), `flutter_secure_storage` for credentials, multi-server / multi-device UX, EventChannel streaming, iOS .xcframework build on macOS, and real-device verification on both platforms. Anchor points:

- `app/lib/screens/` per-screen Dart files.
- `app/lib/state/` Riverpod providers for server list, device list, active session.
- Discovery client that hits `/.well-known/peersh.json` from a hostname.
- EventChannel for streaming ExecResponse chunks (replaces the synchronous Echo on the spike screen).
- Pairing UX implementation (token / QR code, per `open-questions.md` — the Phase 4b plan resolves this).

## Phase 5 — Firebase Auth + FCM Wake-up

Decompose at the start of Phase 5 planning. Anchor points:

- `core/auth/firebase/`, `core/store/firestore/`.
- Firestore schema decision (currently open).
- Cloud Function for FCM wake-up.
- App Check setup.
- Cost guardrails.

## Phase 6 — Background Persistence + Session Resumption

Decompose at the start of Phase 6 planning. Anchor points:

- `SessionManager` on the Windows host with idle timeout and ring buffer.
- Reattach protocol (client presents `session_id`).
- Android Foreground Service.
- iOS Background Modes.
- QUIC keepalive and reconnect policy.

## Phase 7 — Polish, Public Release, and Beyond

Phase 7 scope is deliberately loose. Plan it when we get there.
