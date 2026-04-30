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

### P1-T01: Repository skeleton and `go.work`

- **Scope.** Create the directory structure under the proposed layout (`core/`, `windows/`, `server/`, `proto/`, `scripts/`), plus initial `go.mod` files for the modules touched in Phase 1, plus `go.work` tying them together. Add `LICENSE` and `README.md` (already present from kickoff). Stub package files where needed to make the workspace build.
- **Prerequisites.** None.
- **Validation.** `go work sync` succeeds. `go build ./...` succeeds at this skeleton level (no real logic yet).

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

### P1-T08: `cmd/peersh-cli` REPL client

- **Scope.** A simple REPL CLI binary at a path TBD (likely under `cmd/` of the relevant module — the exact module placement is part of P1-T01). Connects to a `peershd` host given an address. Performs the Hello exchange. Reads lines from stdin, sends as `ExecRequest`, prints streamed `ExecResponse` output until the command finishes, then prompts again. Handles disconnect with a clean error message.
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

Decompose into bounded tasks at the start of Phase 2 planning. Anchor points (subject to Phase 2 plan):

- WebSocket signaling server skeleton (`server/cmd/peersh-signaling/`, `server/ws/`).
- `core/auth/psk/`: HMAC-SHA256 provider and request signing helpers.
- `core/store/sqlite/`: SQLite store with embedded migrations.
- `core/signaling/`: client-side signaling library used by both `peershd` and `peersh-cli`.
- Pairing flow (UX choice TBD; see `open-questions.md`).
- Endpoint exchange protocol on the signaling channel.
- Rate limiting basics.
- Dockerfile, `docker-compose.yml`, `docs/self-hosting.md`.

## Phase 3 — NAT Hole Punching

Decompose at the start of Phase 3 planning. Anchor points:

- `core/punching/` using `pion/stun`.
- IPv6-first / IPv4-fallback strategy.
- Reuse of the punched UDP socket as the QUIC transport (validates P1-T05's external-`PacketConn` design).
- Timeout/retry policy, error reporting on traversal failure.

## Phase 4 — Flutter App + gomobile Integration

Decompose at the start of Phase 4 planning. The phase begins with a **gomobile + quic-go feasibility spike** before substantial Flutter work begins.

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
