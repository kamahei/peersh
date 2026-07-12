# peersh

Run PowerShell on your home Windows PC — or your login shell on a Mac — from your phone, peer-to-peer, without a VPN and without a relay server.

> **Status:** OSS — self-hosted by design. There is no official hosted instance. Bring your own signaling server (or Firebase project) and your own peershd binary. See [Quick start](#quick-start).
>
> **Security implementation status:** the data path is direct QUIC/TLS, signaling does not carry command content, and mTLS is now bound to ed25519-derived device IDs on both ends — clients pin the host's expected `device_id` at the TLS layer and the host requires a client cert. The project is still pre-1.0 and does not yet have an audited production deployment; treat it as beta-quality until 1.0 ships.

## What it is

`peersh` is a tool for driving a home computer's shell from a mobile device (Android / iOS) over the public internet. The host can be a **Windows PC** (running PowerShell) or a **Mac** (running your login shell — zsh / bash). The data path is **direct peer-to-peer** over QUIC with mTLS — your commands and their output never travel through a third-party server. A small signaling server is used **only for connection setup**: it helps the two endpoints find each other, then steps out of the way.

The project is open source under **Apache License 2.0**.

## Two deployment modes

Both modes coexist in a single APK; per-server-entry choice on the mobile side:

- **PSK signaling (simplest).** Run the `peersh-signaling` binary (or the Docker image / Render / Fly / Cloud Run template) anywhere with a public TCP port. HMAC pre-shared keys for auth, SQLite for state. No Google Cloud or Firebase account needed. Five minutes from `docker run` to a working setup.
- **Firebase signaling (Google sign-in).** Same `peersh-signaling` server with `auth_provider = "firebase"` + `store_backend = "firestore"`. Lets the mobile app sign in with Google instead of typing a PSK; backed by Firestore (device / pairing state), Realtime Database (cost-efficient wake-event delivery via SSE — no persistent signaling WebSocket required), and Cloud Functions for the pairing flow. Fits comfortably in the Firebase free tier for personal use; > 100 simultaneous hosts requires the Blaze plan. You provision your own GCP / Firebase project. Walkthrough in [`docs/deploy/firebase.md`](docs/deploy/firebase.md); architecture in [`docs/firebase-mode.md`](docs/firebase-mode.md).

The mobile app supports multiple servers side by side, mixing PSK and Firebase entries.

## Quick start

### Local direct demo (no signaling, no auth — fastest)

Build the host and a test client:

```sh
go build -o bin/peershd.exe ./windows/cmd/peershd
go build -o bin/peersh-cli.exe ./cli/cmd/peersh-cli
```

Run the host on the Windows PC:

```sh
./bin/peershd.exe
```

From the same machine:

```sh
./bin/peersh-cli.exe -addr 127.0.0.1:7777
peersh> Get-Process | Select-Object -First 5 | Out-String
```

For a LAN direct test, start `peershd` with `-listen :7777 -insecure-direct` and pass the host's LAN address to `peersh-cli`. Do not expose direct mode to untrusted networks; signaling mode is the normal remote path.

> **On macOS** the host builds the same way — `bash scripts/build-peershd-macos.sh` (or `GOOS=darwin GOARCH=arm64 go build -o local/peershd ./windows/cmd/peershd`) — then run `./local/peershd`. It spawns your login shell (zsh / bash) instead of PowerShell. See [`docs/deploy/macos-host.md`](docs/deploy/macos-host.md).

### Self-hosted PSK signaling + mobile app

1. **Run the signaling server** somewhere reachable from the public internet. Single-binary self-host: see [`docs/deploy/self-hosting.md`](docs/deploy/self-hosting.md). Render / Fly / Cloud Run templates ship under `server/deploy/`.
2. **Generate a PSK** for yourself and run peershd against the signaling URL: see "PSK lifecycle commands" in the same doc.
3. **Build the Android APK** under `app/`: `flutter build apk --debug`. Sideload via `adb install`. (iOS builds need macOS — see [`.github/workflows/build-ios.yml`](.github/workflows/build-ios.yml) for a CI-side build target you can run after the GitHub Action is configured.)
4. **Add a server entry** in the app: paste the signaling URL + PSK + the device id peershd printed at startup.

### Self-hosted Firebase signaling

For the Google-sign-in flow with multi-PC picker etc., follow [`docs/deploy/firebase.md`](docs/deploy/firebase.md). It includes the GCP / Firebase project setup, Cloud Run deploy, Cloud Functions, mobile FlutterFire config, and the peershd browser-sign-in / pair-code flows.

## Tech direction

- **Backend (host, signaling, mobile network layer):** Go.
- **Mobile UI:** Flutter (Dart). The Go network layer is shared across all clients via `gomobile bind` (`.aar` for Android, `.xcframework` for iOS), called from Dart through Method Channel / EventChannel.
- **Transport:** QUIC over UDP via `github.com/quic-go/quic-go`. TLS 1.3 mandatory.
- **Terminal backend:** a real PTY on the host — ConPTY on Windows, forkpty on macOS (`github.com/creack/pty`) — so the interactive session behaves like a native terminal.
- **NAT traversal:** UDP hole punching with `pion/stun`. **No relay/TURN.** When traversal cannot succeed (e.g. CGNAT on both sides), peersh fails with an actionable error rather than relaying.
- **Wire format:** Protobuf (`google.golang.org/protobuf`).
- **Pluggable auth:** `none` / `psk` / `firebase` behind `auth.Provider` (in `core/auth/`).
- **Pluggable storage:** in-memory / SQLite / Firestore behind `store.Store` (in `core/store/`).
- **Mobile UI integration:** FlutterFire when Firebase auth is selected.

## Documentation map

Top-level: [`docs/`](docs/). Quick links:

- **For users:** [`docs/build.md`](docs/build.md) (build everything from source), [`docs/user-manual.md`](docs/user-manual.md) (mobile app), [`docs/backup.md`](docs/backup.md) (backing up local secrets).
- **For operators:** [`docs/deploy/`](docs/deploy/) — Cloud Run, Render, generic self-hosting, Firebase mode, [macOS host](docs/deploy/macos-host.md).
- **For contributors:** [`docs/design/`](docs/design/) — project overview, architecture, data model, glossary. Plus [`AGENTS.md`](AGENTS.md) and [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Environment

- **Dev machine.** Windows / macOS / Linux. Go 1.22+, Flutter (latest stable), JDK 17 for Android, Xcode for iOS.
- **Target host.** **Windows 10 / 11** — PowerShell 7 (`pwsh`) preferred; falls back to `powershell.exe` if `pwsh` is not on PATH. **Or macOS** — spawns your login shell (zsh / bash, resolved from `$SHELL`); PowerShell is not required.

## License

Apache License 2.0. See `LICENSE`.
