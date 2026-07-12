# Building peersh from source

peersh has three buildable components:

| Component | Where | Output |
|---|---|---|
| `peersh-signaling` (signaling server) | `server/` Go module | `peersh-signaling` binary or Docker image |
| `peershd` + `peersh-cli` (host + CLI) | `windows/cmd/peershd`, `cli/cmd/peersh-cli` | Windows `.exe` / macOS Mach-O binaries |
| Mobile app | `app/` (Flutter) | Android `.apk` (iOS build needs macOS) |

The mobile app additionally depends on a regenerated `mobile-core` AAR / xcframework produced by `scripts/build-mobile-core.{sh,cmd}` (gomobile bind).

## Common prerequisites

- **Go 1.22+** on PATH.
- **Git Bash** (Windows) or any POSIX shell (macOS / Linux) for the `scripts/*.sh` helpers.
- **`buf` and `protoc-gen-go`** only if you change `.proto` files:
  ```sh
  go install github.com/bufbuild/buf/cmd/buf@latest
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  scripts/gen-proto.sh
  ```

## peersh-signaling

```sh
go build -o bin/peersh-signaling ./server/cmd/peersh-signaling
```

That's it for a local-run binary. For a Docker image:

```sh
docker build -f server/deploy/Dockerfile -t peersh-signaling:dev .
```

Or use a deploy template (`server/deploy/cloud-run/`, `server/deploy/render.yaml`, `server/deploy/fly.toml`); see [`deploy/`](deploy/).

### Run the tests

```sh
go test ./core/... ./server/...
```

Cross-phase invariant tests live under `core/` (transport contract, signaling protocol round-trips, auth providers, store implementations).

## peershd + peersh-cli (Windows + macOS host)

`peershd` is a host on both Windows and macOS — the whole `windows/` module compiles for both, and `core/` is fully portable. Only the PTY backend and shell resolver differ: ConPTY + PowerShell on Windows, `forkpty` (`github.com/creack/pty`) + your login shell on macOS. The Windows build steps below are followed by a [macOS host build](#macos-host-build) subsection.

### Plain build (PSK only — no Firebase embedding)

From any platform with Go (cross-compiles to Windows fine):

```sh
GOOS=windows GOARCH=amd64 go build -o local/peershd.exe ./windows/cmd/peershd
GOOS=windows GOARCH=amd64 go build -o local/peersh-cli.exe ./cli/cmd/peersh-cli
```

`local/` is gitignored. Move the binaries to your Windows host (or build directly on Windows; same command without the `GOOS` / `GOARCH`).

### Distribution build with embedded Firebase / OAuth defaults

End users running the resulting binary won't need to pass `-firebase-*` / `-google-*` flags. See [`deploy/self-hosting.md`](deploy/self-hosting.md) "Distributing peershd with embedded Firebase defaults" for the full workflow; the short version:

```sh
cp scripts/peershd-build.env.example local/peershd-build.env
$EDITOR local/peershd-build.env       # paste your Firebase / OAuth values
bash scripts/build-peershd-distrib.sh # or scripts\build-peershd-distrib.cmd
```

Output: `local/peershd.exe`. Verify the embedded values:

```sh
strings local/peershd.exe | grep -E "AIza|cloudfunctions|googleusercontent"
```

### Auto-start peershd at logon or boot

Helper scripts under `scripts/` wrap `schtasks` / `sc.exe`. Each pair
is a thin `.cmd` wrapper plus a `.ps1` that does the actual work:

| What | Install | Uninstall | Install dir | Runs as |
|---|---|---|---|---|
| Per-user logon task | `scripts\install-peershd-task.cmd` | `scripts\uninstall-peershd-task.cmd` | `%LOCALAPPDATA%\peersh` | the user who installed it (no admin needed) |
| Windows service | `scripts\install-peershd-service.cmd` | `scripts\uninstall-peershd-service.cmd` | `C:\Program Files\peersh` | LocalSystem (run elevated) |

Both install flows:

1. Pick the source binary (default `<repo>\local\peershd.exe`; pass an absolute path as the first argument to override).
2. Copy `peershd.exe` into the install directory above so the script tree can be moved or deleted afterwards.
3. In Firebase mode, run `peershd -firebase-login -firebase-login-only` once to open a Google sign-in browser window. The persisted refresh token (in the install directory) is reused on every subsequent launch — no further prompts. PSK / non-Firebase binaries skip this step automatically.
4. Register the scheduled task / service. The logon task runs hidden via `wscript.exe` + a generated `.vbs` launcher (no console flash) and writes stdout / stderr to `<install-dir>\logs\peershd.log`. The service variant locks down the refresh token under `C:\Program Files\peersh\` to SYSTEM + Administrators.

Uninstall removes the task / service and stops any running peershd from the install directory; pass `/remove-files` (or `--remove-files`) to delete the install directory too.

### macOS host build

On macOS `peershd` spawns your **login shell** (zsh / bash / sh, resolved from `$SHELL`) under a `forkpty` PTY instead of PowerShell under ConPTY. The interactive PTY path — the real terminal — works fully; the legacy one-shot `exec.v1` (PowerShell) path is **Windows-only**, so a client that requests it against a Mac gets a clean "not available" response.

Plain build (no Firebase embedding), from a Mac or any host with Go:

```sh
GOOS=darwin GOARCH=arm64 go build -o local/peershd ./windows/cmd/peershd
```

Distribution build with embedded Firebase / OAuth defaults (reads `local/peershd-build.env`, the same file the Windows `build-peershd-distrib.sh` uses):

```sh
cp scripts/peershd-build.env.example local/peershd-build.env
$EDITOR local/peershd-build.env                  # paste your Firebase / OAuth values
bash scripts/build-peershd-macos.sh              # → local/peershd (Mach-O)
bash scripts/build-peershd-macos.sh universal    # arm64 + amd64 fat binary
```

#### Auto-start at login (LaunchAgent)

`scripts/install-peershd-macos.sh` builds peershd, installs it to `~/.local/bin/peershd`, bootstraps Firebase auth once (browser sign-in, or set `PEERSH_PAIR_CODE` to use the mobile app's pair code instead), and registers a **per-user LaunchAgent** at `~/Library/LaunchAgents/peershd.plist` (`RunAtLoad` + `KeepAlive`) via kardianos/service. It runs as the logged-in user — not root — so it inherits your shell, env, and keychain (the macOS analogue of the Windows logon task). Manage it with:

```sh
peershd -install | -uninstall | -start | -stop | -service-status
```

Remove everything with `bash scripts/uninstall-peershd-macos.sh`. Logs go to `~/peershd.out.log` / `~/peershd.err.log`. Full walkthrough in [`deploy/macos-host.md`](deploy/macos-host.md).

### Run the tests

```sh
go test ./core/... ./windows/...
```

The `windows/` module builds and tests on both Windows and macOS; the PTY backend lives behind build-tagged files (`pty_windows.go`, `pty_darwin.go`, `pty_other.go`).

## Mobile app — Android

### Prerequisites (Android-specific)

- **JDK 17** on PATH (`java -version`).
- **Android SDK 34** + NDK + platform-tools. The `android-actions/setup-android@v3` action in CI uses the same package list — replicate it locally via Android Studio's SDK Manager.
- **Flutter** stable.
- **gomobile**:
  ```sh
  go install golang.org/x/mobile/cmd/gomobile@latest
  gomobile init
  ```

### Optional: FlutterFire config (Firebase mode)

Run once per machine; only needed if you want the Firebase code path to actually work:

```sh
dart pub global activate flutterfire_cli
cd app
flutterfire configure --project=<your-project-id> --platforms=android
```

This writes `app/lib/firebase_options.dart`, `app/android/app/google-services.json`, and `app/firebase.json`. To keep them out of `git diff`:

```sh
git update-index --skip-worktree \
  app/lib/firebase_options.dart \
  app/android/app/google-services.json \
  app/firebase.json
```

(All three have committed stubs so the build still succeeds when they are not replaced — the app just doesn't initialise Firebase.)

### Build the gomobile AAR

```sh
bash scripts/build-mobile-core.sh android
```

Output: `app/android/app/libs/peersh.aar`. Re-run whenever you change anything under `mobile-core/` or `core/`.

### Build the APK

```sh
cd app
flutter pub get
flutter build apk --debug          # sideload-only, debug-keystore-signed
# or
flutter build apk --release        # release build; debug-keystore-signed by default
```

Output: `app/build/app/outputs/flutter-apk/app-{debug,release}.apk`. Install via:

```sh
adb install -r build/app/outputs/flutter-apk/app-debug.apk
```

#### Distribution build (real Firebase config swap-in)

The committed `app/lib/firebase_options.dart`, `app/android/app/google-services.json`, and `app/firebase.json` are OSS placeholders that throw at runtime if a Firebase server entry is opened. To produce an APK that boots Firebase mode, save your operator-specific FlutterFire output as `local/firebase_options.dart.real`, `local/google-services.json.real`, and `local/app-firebase.json.real`, then build with the wrapper:

```sh
bash scripts/build-apk-distrib.sh        # macOS / Linux / Git Bash
.\scripts\build-apk-distrib.cmd          # Windows native
```

The script swaps the real files in, runs `flutter build apk --release`, and restores the placeholders via `git checkout` on exit so the secrets never appear in `git status`.

### Release-signing with your own keystore

By default `flutter build apk --release` uses the debug keystore (sideload-fine, Play-Store-rejected). To use your own upload key, copy `app/android/key.properties.example` to `app/android/key.properties` and fill in. See [`backup.md`](backup.md) for keystore-handling guidance.

```sh
keytool -genkey -v -keystore app/android/release.keystore \
  -alias peersh-upload -keyalg RSA -keysize 2048 -validity 10000
```

After dropping `key.properties` and `release.keystore` into `app/android/`, `flutter build apk --release` picks them up automatically.

## Mobile app — iOS

iOS builds need **macOS** with Xcode. The native MethodChannel / EventChannel bridge to mobile-core is now implemented — the shared Swift file `app/shared/apple/PeershBridge.swift`, wired into the iOS Runner — so the iOS client is functional, not the old `IOS_BIND_PENDING` stub. Same gomobile workflow, swap the target:

```sh
bash scripts/build-mobile-core.sh apple    # aliases: ios, macos
cd app
flutter pub get
flutter build ios --simulator --debug      # simulator build
```

`build-mobile-core.sh apple` produces `app/shared/apple/Frameworks/peersh.xcframework` carrying both the **iphoneos** and **iphonesimulator** slices, embedded by the iOS Runner. (This replaces the old `app/ios/Frameworks/` output path; macOS is a peersh *host*, not a Flutter client, so there is no macOS client slice and no `app/macos/`.) The Firebase iOS SDK is pulled in via **Swift Package Manager** automatically — recent Flutter needs no manual `pod install`.

iOS Firebase config (`GoogleService-Info.plist`, the `firebase_options.dart` iOS entry, and the `GIDClientID` + reversed-client URL scheme in `Info.plist`) comes from `flutterfire configure --platforms=android,ios` (or the Firebase console):

```sh
cd app
flutterfire configure --project=<your-project-id> --platforms=android,ios
```

Code-signing for distribution requires an Apple Developer account, which is out of scope for the OSS source; a simulator or developer-mode device build needs none.

## CI (GitHub Actions)

Three manually-triggered workflows ship under `.github/workflows/`:

- `build-peershd.yml` — Windows binary builds.
- `build-android.yml` — Android APK builds (debug / release).
- `build-ios.yml` — iOS xcframework + unsigned `.app` (macOS runner).

Plus a tag-triggered `release.yml` that creates a GitHub Release with `peershd-windows-amd64.exe`, its `.sha256`, and `peersh-android.apk` attached.

See [`.github/workflows/README.md`](../.github/workflows/README.md) for the optional secrets that wire embedded Firebase defaults into CI builds.

## Verify everything works

A quick smoke test after a full build:

```sh
# 1. Run signaling locally with PSK auth + in-memory store.
bin/peersh-signaling serve --insecure-http
# (in another terminal)
bin/peersh-signaling psk add --user alice --label desktop

# 2. Run peershd against it.
local/peershd.exe -signaling ws://localhost:8443/ws -user alice \
  -psk-file local/alice.psk -display-name "test-pc"
# Note the device_id printed at startup.

# 3. Run peersh-cli to verify end-to-end.
local/peersh-cli.exe -signaling ws://localhost:8443/ws -user alice \
  -psk-file local/alice.psk -target <device-id>
peersh> Get-Process | Select-Object -First 3 | Out-String
```

If this works, you've got a complete chain. The mobile app uses the same signaling endpoint with the same PSK (entered via "Add server" → Auth mode = PSK).
