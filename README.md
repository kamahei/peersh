# peersh

Run PowerShell on your home Windows PC from your phone, peer-to-peer, without a VPN and without a relay server.

> **Status:** OSS — self-hosted by design. There is no official hosted instance. Bring your own signaling server (or Firebase project) and your own peershd binary. See [Quick start](#quick-start).
>
> **Security implementation status:** current builds are experimental. The data path is direct QUIC/TLS and signaling still does not carry command content, but full peer certificate verification / mTLS binding is not complete yet. Do not treat the current source as a production-ready remote shell until that work lands.

## What it is

`peersh` is a tool for executing PowerShell commands on a home Windows PC from a mobile device (Android / iOS) over the public internet. The data path is **direct peer-to-peer** over QUIC with mTLS — your commands and their output never travel through a third-party server. A small signaling server is used **only for connection setup**: it helps the two endpoints find each other, then steps out of the way.

The project is open source under **Apache License 2.0**.

## Two deployment modes

Both modes coexist in a single APK; per-server-entry choice on the mobile side:

- **PSK signaling (simplest).** Run the `peersh-signaling` binary (or the Docker image / Render / Fly / Cloud Run template) anywhere with a public TCP port. HMAC pre-shared keys for auth, SQLite for state. No Google Cloud or Firebase account needed. Five minutes from `docker run` to a working setup.
- **Firebase signaling (Google sign-in).** Same `peersh-signaling` server with `auth_provider = "firebase"` + `store_backend = "firestore"`. Lets the mobile app sign in with Google instead of typing a PSK; backed by Firestore + FCM wake-up + Cloud Functions for the pairing flow. Fits comfortably in the Firebase free tier at low-thousands-of-users scale; you provision your own GCP / Firebase project. Walkthrough in [`docs/deploy/firebase.md`](docs/deploy/firebase.md).

The mobile app supports multiple servers side by side, mixing PSK and Firebase entries.

## Quick start

### Local LAN demo (no signaling, no auth — fastest)

Build the host and a test client:

```sh
go build -o bin/peershd.exe ./windows/cmd/peershd
go build -o bin/peersh-cli.exe ./cli/cmd/peersh-cli
```

Run the host on the Windows PC:

```sh
./bin/peershd.exe -listen :7777
```

From any machine on the same LAN:

```sh
./bin/peersh-cli.exe -addr <host-ip>:7777
peersh> Get-Process | Select-Object -First 5 | Out-String
```

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
- **NAT traversal:** UDP hole punching with `pion/stun`. **No relay/TURN.** When traversal cannot succeed (e.g. CGNAT on both sides), peersh fails with an actionable error rather than relaying.
- **Wire format:** Protobuf (`google.golang.org/protobuf`).
- **Pluggable auth:** `none` / `psk` / `firebase` behind `auth.Provider` (in `core/auth/`).
- **Pluggable storage:** in-memory / SQLite / Firestore behind `store.Store` (in `core/store/`).
- **Mobile UI integration:** FlutterFire when Firebase auth is selected.

## Documentation map

Top-level: [`docs/`](docs/). Quick links:

- **For users:** [`docs/build.md`](docs/build.md) (build everything from source), [`docs/user-manual.md`](docs/user-manual.md) (mobile app), [`docs/backup.md`](docs/backup.md) (backing up local secrets).
- **For operators:** [`docs/deploy/`](docs/deploy/) — Cloud Run, Render, generic self-hosting, Firebase mode.
- **For contributors:** [`docs/design/`](docs/design/) — project overview, architecture, data model, glossary. Plus [`AGENTS.md`](AGENTS.md) and [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Environment

- **Dev machine.** Windows / macOS / Linux. Go 1.22+, Flutter (latest stable), JDK 17 for Android, Xcode for iOS.
- **Target host.** Windows 10 / 11. PowerShell 7 (`pwsh`) preferred; falls back to `powershell.exe` if `pwsh` is not on PATH.

## License

Apache License 2.0. See `LICENSE`.
