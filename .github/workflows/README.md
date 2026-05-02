# peersh GitHub Actions

All workflows here are **manually triggered** (`workflow_dispatch`); none run automatically on push or PR. The intent is for the project owner to launch a build when they want artefacts (a new peershd.exe, an APK to install, an unsigned iOS bundle), then download from the run's "Artifacts" tab.

| Workflow | Runner | Purpose |
|---|---|---|
| `build-peershd.yml` | windows-latest | peershd.exe + peersh-cli.exe (+ optional MSI) |
| `build-android.yml` | ubuntu-latest | Flutter Android APK (debug or release) |
| `build-ios.yml` | macos-latest | iOS xcframework + unsigned Flutter `.app` bundle |

## Optional repo secrets

All secrets below are optional — the workflows succeed without them, just producing a Firebase-less / unsigned build.

### peershd Firebase embeddings

Set these to bake operator defaults into the binary so end users don't need to pass `-firebase-*` / `-google-*` flags. See `scripts/build-peershd-distrib.sh` for the local equivalent.

- `PEERSHD_FIREBASE_API_KEY` — Firebase Web API key (public-by-design).
- `PEERSHD_FIREBASE_PROJECT_ID` — Firebase project id.
- `PEERSHD_FIREBASE_REGION` — Cloud Functions region (e.g. `asia-northeast1`).
- `PEERSHD_SIGNALING_URL` — `wss://<your-cloud-run-host>/ws`.
- `PEERSHD_GOOGLE_CLIENT_ID` — OAuth 2.0 "Desktop app" client id.
- `PEERSHD_GOOGLE_CLIENT_SECRET` — same client's secret (Installed-app secret, not actually secret per Google's docs).

### Mobile FlutterFire config (Android + iOS)

Set these to produce a Firebase-functional build. Each value is **base64** of the corresponding file's contents.

- `GOOGLE_SERVICES_JSON` — `app/android/app/google-services.json`.
- `FIREBASE_OPTIONS_DART` — `app/lib/firebase_options.dart`.
- `APP_FIREBASE_JSON` — `app/firebase.json`.
- `GOOGLE_SERVICE_INFO_PLIST` — `app/ios/Runner/GoogleService-Info.plist` (iOS only).

Encode locally via `base64 -w0 <file>` (Linux) or `[Convert]::ToBase64String([IO.File]::ReadAllBytes("<file>"))` (PowerShell). Paste the resulting string into the secret value.

## What's deliberately NOT here yet

- **Auto-trigger on push or release tag.** All three workflows are manual. Once the project starts cutting numbered releases, a release.yml will read git tags and chain into these via `workflow_call`.
- **iOS code signing.** No `.p12` import / xcodebuild archive yet — happens once an Apple Developer account is wired up.
- **Android Play upload signing.** Release APKs are debug-keystore-signed in CI; the Play upload key import lands when the Play Store track is opened.
- **Caching policy beyond the actions' built-ins.** Phase 7 polish.
