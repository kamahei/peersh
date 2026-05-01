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

> **Status: shipped.** Phase 2 landed the `server/` Go module (`peersh-signaling` binary with `serve` + `psk {add,list,revoke}` subcommands), the `core/auth/psk` HMAC-SHA256 provider with replay protection, the `core/store/sqlite` modernc-backed store with embedded migrations, the `core/signaling` WebSocket client, the protobuf-binary signaling channel under `proto/peersh/signal/v1/`, per-IP / per-user / per-device rate limiting, TOML + env-var configuration, and Dockerfile + docker-compose.yml + `docs/deploy/self-hosting.md` for self-hosting. Pairing is **implicit under shared PSK user_id**; explicit token / QR pairing arrives with the mobile app in Phase 4.

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
- T17: docs/deploy/self-hosting.md.
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

### Phase 4b — Real Mobile UI

> **Status: shipped on Android (toolchain green, real-device run pending).** Phase 4b extended `mobile-core` with `Session` + `Output` + `OpenDirectSession` / `OpenSignalingSession` / `Exec` / `ReadFile` / `Close`, rebuilt the AAR, added the streaming MethodChannel + EventChannel bridge in Kotlin (Swift stub on iOS), and built the Dart UI: `ServersScreen`, `ServerEditorScreen` (with `/.well-known/peersh.json` discovery prefill), `TerminalScreen` (with the three peersh-parity features below), `ImeInputSheet`, `TextViewerScreen`, `SettingsScreen`. State is persisted via `flutter_secure_storage` behind Riverpod providers. `flutter analyze` is clean, smoke widget test passes, and `flutter build apk --debug` produces a debug APK including the extended AAR.

The three peersh-parity features the user asked for ship here:
- Wrap-vs-horizontal-scroll toggle on the terminal screen (AppBar icon; per-screen override on top of the persisted default).
- IME input bottom sheet (modal, multiline, Append-Enter switch, Send button).
- Built-in text viewer (search + match navigation + copy-all + encoding/size meta), reachable from the terminal AppBar; backed by `mobile-core.Session.ReadFile`.

iOS `.xcframework` build and real-device verification on either platform require hardware the user cannot reach remotely; both are deferred and picked up next time on-LAN.

Phase 4b was decomposed into P4b-T01 through P4b-T13:

- T01: mobile-core Session API (`Output`, `Session`, `OpenDirectSession`, `OpenSignalingSession`, `Exec`, `ReadFile`, `Close`).
- T02: rebuild AAR (`gomobile bind -target=android` over the extended package).
- T03: Kotlin + Swift native bridges (session map + `EventChannel` sink + Output adapter on Android; commented stub on iOS).
- T04: Dart bridge + models (`ServerEntry`, `SessionEvent`) + secure store + Riverpod state.
- T05: discovery client + server editor with hostname prefill.
- T06: servers screen (list, add, edit, swipe-to-delete).
- T07: terminal screen scaffold + active session lifecycle.
- T08: `LogView` widget with wrap/scroll toggle.
- T09: IME input bottom sheet.
- T10: text viewer screen.
- T11: settings screen + app shell wiring.
- T12: tests + APK build verify.
- T13: doc reconciliation.

Original anchor points (now resolved or carried into the deferred follow-up):

- `app/lib/screens/` per-screen Dart files.
- `app/lib/state/` Riverpod providers for server list, device list, active session.
- Discovery client that hits `/.well-known/peersh.json` from a hostname.
- EventChannel for streaming ExecResponse chunks (replaces the synchronous Echo on the spike screen).
- Pairing UX implementation (token / QR code, per `open-questions.md` — the Phase 4b plan resolves this).
- **peersh-parity terminal ergonomics** (required, not stretch). Reference `peersh/mobile/lib/terminal/` and `peersh/mobile/lib/screens/terminal_workspace.dart` for known-good shapes:
  - Wrap-vs-scroll toggle on the terminal screen. Wrap mode lets xterm autoresize to viewport; scroll mode pins the terminal at 120 columns inside a horizontal `SingleChildScrollView`. PowerShell remote PTY is widened to ≥ 120 columns when wrap is active so output formatting stays sane (`remoteColsFor` pattern).
  - IME input bottom sheet. A floating button on the terminal screen opens a modal bottom sheet with a multiline `TextField` (`TextInputType.multiline`, `TextInputAction.newline`), an "append Enter" toggle, and a Send button. On send, normalize line endings (`\r\n`/`\n` → `\r`) and forward to the terminal as input.
  - Built-in simple text viewer screen. Takes a remote-host file path, fetches content via `Get-Content -Raw -Encoding UTF8 <path>` over the existing exec stream (no protocol change), and renders it with: search field with next/previous navigation and match counter, optional syntax highlighting toggle, copy-all action, encoding + size meta in the header.

## Phase 5 — Firebase Auth + FCM Wake-up

> **Status: server-side shipped; mobile FlutterFire deferred to Phase 5b.** Phase 5 added `core/auth/firebase` (Admin-SDK ID-token verification with stubbed-TokenVerifier tests), `core/store/firestore` (Cloud Firestore Store implementation; PSK methods are no-ops), a `firebase_id_token` field on the `Register` proto, server config switches `auth_provider = "psk" | "firebase"` and `store_backend = "sqlite" | "firestore"` with validation, the `firebase/` Firebase project artifacts (firebase.json, firestore.rules with per-user isolation, firestore.indexes.json, functions/ TypeScript Cloud Function `onSessionCreated` triggering FCM wake-up on session creation), and `docs/deploy/firebase.md` for operators.

Phase 5 was decomposed into P5-T01 through P5-T05:

- T01: `core/auth/firebase` — Provider, Credentials, NewFromServiceAccount; tests with a TokenVerifier stub.
- T02: `core/store/firestore` — Firestore Store implementation; PSK methods return ErrNotFound.
- T03: server config + main wiring — `[firebase]` block, `auth_provider` / `store_backend` switches, `peersh-signaling` builds the right provider + store at startup. Proto adds `firebase_id_token` to `Register`.
- T04: `firebase/` project — `firestore.rules`, Cloud Function `onSessionCreated` triggering FCM wake-up.
- T05: `docs/deploy/firebase.md` + doc reconciliation.

Phase 5b anchor points (next session, after on-LAN access and a real Firebase project):

- FlutterFire integration in `app/`: `firebase_core`, `firebase_auth`, `cloud_firestore`, `firebase_messaging`, `firebase_app_check`. Add an opt-in build flavor so the default APK still builds without a `google-services.json`.
- Mobile sign-in screen + Firestore-backed device discovery.
- Host-side FCM token registration (`peershd` writes `fcm_token` on its `users/{uid}/devices/{deviceId}` doc).
- App Check enforcement on the server (reject Register without a valid App Check token).
- Cost guardrail Cloud Function (auto-disable `onSessionCreated` when budget alert fires).
- Cost guardrails.

## Phase 6 — Background Persistence + Session Resumption

> **Status: server-side reattach shipped; client-side persistence + ring-buffer replay deferred to Phase 6b.** Phase 6 added `pwsh.SessionManager` (a long-lived map of `session_id → pwsh.Host` with a 30-minute idle timeout and a periodic `Sweep`), extended `ClientHello` with an optional `session_id` and `ServerHello` with `session_id` + `reattached`, and rewired `peershd`'s `serveConn` so the QUIC connection's `pwsh.Host` is owned by the manager. A reconnecting client that presents the same `session_id` resumes the same shell process (cwd + variables intact); an unknown / expired session_id transparently produces a fresh session. QUIC keepalive remains at the Phase 1 default (15 s).

The ring-buffer replay (output emitted while the client was disconnected), Android Foreground Service, iOS Background Modes, and app-side automatic reconnect with exponential backoff are all client-side and require either macOS (iOS BG Modes) or on-LAN testing on a real Android device (Foreground Service); they're parked under Phase 6b.

Phase 6 was decomposed into P6-T01 through P6-T04:

- T01: proto — `session_id` on ClientHello, `session_id` + `reattached` on ServerHello.
- T02: `windows/pwsh.SessionManager` with `AttachOrCreate` / `Detach` / `Sweep` / `Run` / `Close`. Tests cover fresh-create, reattach, unknown id, expiry sweep.
- T03: peershd `doHandshake` returns `(session_id, *pwsh.Host)`; `serveConn` keeps the manager-owned host and detaches on QUIC close instead of killing pwsh.
- T04: doc reconciliation.

### Phase 6b — Multi-tab terminal + PTY persistence

> **Status: shipped (Tier 1 + Tier 2).** Tier 1 replaced the single-PTY `TerminalScreen` with a tabbed view (`TerminalTabsScreen` + `TerminalPane`) that hosts multiple terminals against the same QUIC connection; tabs survive switches via `IndexedStack`. Tier 2 added server-side PTY persistence via `windows/ptyhost.Manager` (256 KiB scrollback ring buffer per Session, 30-minute idle TTL, periodic sweep), the `PTYReattachAck` frame as the first server-bound frame on every PTY stream, `FilesRequest.{ListPTYs,KillPTY}` RPCs, and the mobile reattach UI (bottom-sheet picker on connect / "+" tab; long-press tab → "Close (keep alive)" or "Kill PTY"). Reattach handles persist via `flutter_secure_storage` so the user can leave + return + rebind to the same shell. Tab labels auto-update from the OSC 9;9 cwd via 2 s polling.
>
> Phase 6b was decomposed into P6b-T01 through P6b-T10:
>
> - T01: TerminalTabsScreen with multi-PTY (IndexedStack-backed tab list).
> - T02: TerminalPane widget extraction (one xterm Terminal + PTY lifecycle per tab).
> - T03: Tab title from cwd / shell command.
> - T04: Routing change ServersScreen → TerminalTabsScreen.
> - T05: Tier 1 build verify + commit + push.
> - T06: PTY persistence + scrollback ring buffer (`ptyhost.Manager`).
> - T07: PTY reattach protocol + dispatch (`PTYReattachAck`, `ListPTYs`, `KillPTY`).
> - T08: mobile-core reattach API (`Session.OpenPTYReattach`, `ListPTYs`, `KillPTY`).
> - T09: Tabs screen reattach UI (picker, persisted handles, long-press menu).
> - T10: Tier 2 build verify + commit + push.

Remaining Phase 6b anchor points (deferred):

- Android Foreground Service plugin (or native) so the active session can survive app backgrounding within OS limits.
- iOS Background Modes + the App-Store-review caveat work.
- Mobile-side auto-reconnect with exponential backoff. Today the user must tap "Retry" after a connection drops; reattach will pick up the same shells once reconnected.
- App-side "session resumed" banner in the terminal screen.
- Per-host scrollback size override (today the cap is the server-side default 256 KiB).

## Phase 8 — Interactive PTY terminal + session-scoped file API

> **Status: shipped (Tier 1 + Tier 2).** Tier 1 replaced the one-shot Exec model on the mobile terminal with a full ConPTY-backed pseudo-console: `windows/pty` ConPTY wrapper, `windows/shell` resolver that wraps PowerShell / cmd with an OSC 9;9 prompt emitter, `proto/peersh/v1/exec.proto` extended with `StreamRequest` envelope dispatching between the legacy `ExecRequest` and the new `PTYRequest` / `PTYInput` / `PTYResize` / `PTYData` / `PTYExit` messages (plus `PTYFrame` multiplexing). `peershd` advertises `capabilities = ["pty.v1"]` after bumping `protocol_version` to 2. The mobile-core gained `Session.OpenPTY` + `PTYSession.Write`/`Resize`/`Close`; Flutter switched to `xterm.dart 4.0` for ANSI rendering, added a soft-keyboard-friendly special-keys bar (Esc/Ctrl/Tab/arrows/^C^D^L^Z/PgUp/PgDn/Home/End plus an IME-input launcher at the leftmost slot mirroring peersh), and ports the `resize_policy.dart` / `viewport_estimate.dart` helpers from peersh so PowerShell's startup cwd-cache doesn't lock the PTY at xterm's default 80x24. peersh-cli grew a `-pty` mode for fast end-to-end protocol verification on the host machine.
>
> Tier 2 added cwd tracking and a session-scoped file browser/viewer: `windows/session.CWDTracker` (port of peersh's OSC 9;9 state machine, with chunk-boundary handling and a 4 KiB body cap), `ptyhost.Session.CWD()` exposed via `Session.GetCWD`, and the new `FilesRequest` / `FilesResponse` envelope on the existing per-stream wire format (capability `files.v1`). New mobile screens: `FileBrowserScreen` (session-scoped, no operator-configured roots, no bookmarks — peersh's design intentionally narrows the scope) and a rewritten `TextViewerScreen` (`.forSession` constructor with the new `bridge.readSessionFile` RPC; syntax-highlight toggle via `flutter_highlighting`; encoding + size + truncated-badge meta strip; copy-all and search nav).
>
> Phase 8 was decomposed into P8-T01 through P8-T22:
>
> - T01–T11: Tier 1 (ConPTY + shell wrapper + protocol + mobile xterm + special-keys bar + peersh-cli mode + verify).
> - T12: Tier 1 real-device test (claude / codex CLIs verified through arrow keys / Ctrl+C / resize).
> - T13–T15: Tier 2 server-side (cwdtracker + Session.CWD + Files protocol).
> - T16: peershd Files dispatch (resolveSessionPath enforces cwd containment; UTF-16 BOM transcode to UTF-8).
> - T17–T18: mobile-core + bridge for files API.
> - T19: `flutter_highlighting` + ported `syntax_highlighting.dart`.
> - T20: `FileBrowserScreen.forSession`.
> - T21: `TextViewerScreen` rewrite (peersh parity).
> - T22: `TerminalScreen` AppBar entry point for the file browser.

## Security hardening (carried throughout)

> **Status: ongoing as items emerge.** Notable item from Phase 8 / Cloud Run rollout:
>
> - `/metrics` endpoint gated behind `PEERSH_SIGNALING_METRICS_TOKEN` (fail-closed: empty token → 404, otherwise `Authorization: Bearer <token>` required, ConstantTimeCompare). Public-access Cloud Run + an unauthenticated `/metrics` would have leaked Go runtime version + active-session counts + per-reason auth-failure rates to the entire internet; the token gate keeps Prometheus telemetry private without disabling the endpoint for legitimate scrapers.

## Phase 7 — Polish, Public Release, and Beyond

> **Status: in progress.** Phase 7 ships incrementally; items land as they become tractable from the current dev environment. Done so far:
>
> - peershd Windows Service registration via `kardianos/service` (`-install`, `-uninstall`, `-start`, `-stop`, `-service-status`).
> - peershd Windows Scheduled-Task (logon-time) registration via `schtasks.exe` (`-install-logon-task`, `-uninstall-logon-task`, `-logon-task-user <name>`). The two installation modes are independent: pick service for SYSTEM-context background operation, or logon-task to run as the user who logged in.
> - Prometheus `/metrics` endpoint on `peersh-signaling` (counters for upgrade / register / connect; gauge for active connections).
> - `SECURITY.md` (responsible disclosure path, in-scope / out-of-scope, trust-model snapshot).
> - `CONTRIBUTING.md` (build commands, conventions, cross-phase invariants).
> - `server/deploy/render.yaml` and `server/deploy/fly.toml` for one-click self-hosting deploys.
>
> Phase 7 work that remains and is not yet tractable from this remote-only Windows dev box:
>
> - Auto-update for the Windows binaries.
> - App Store / F-Droid metadata + signing pipelines (the Google Play side is drafted under `docs/store/`).
> - OIDC / Postgres backends if community demand emerges.
> - Logo / website / docs site (icon now ships at `app/icon/peersh_imagegen_1024.png`; website is still TBD).
> - Spawn-pwsh-as-active-user for service mode (WTSQueryUserToken + CreateProcessAsUser). Currently the logon-task path covers the same use case more simply.
>
> Phase 7 work additionally shipped (post-Phase-8):
>
> - **MSI installer** under `windows/installer/peersh.wxs` (WiX 4 / 7) plus the driver `scripts/build-msi.cmd`. Builds an 8 MB MSI containing peershd.exe + peersh-cli.exe with PATH entry, Start menu shortcut, Apache 2.0 license dialog, and Add/Remove Programs registration. Service registration stays explicit-opt-in via `peershd -install` afterward.
> - **xterm.dart full ANSI rendering** — replaced the Phase 4b `LogView` in Phase 8 Tier 1.
> - **Token-gated `/metrics`** (Phase 8 sidecar). `PEERSH_SIGNALING_METRICS_TOKEN` empty → 404; set → bearer required.
