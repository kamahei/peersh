# Backup of local data

peersh keeps a number of operator-specific files **outside** git on purpose — they're either secrets (PSK secrets, service-account keys), per-machine-generated (Android keystore signing identity, debug certs), or per-Firebase-project (FlutterFire config). Most of them you can re-generate; some you cannot, and losing them means dependents can't reach the system any more (the mobile app no longer recognises a re-built APK; previously-paired peershds have to re-pair, etc.).

This doc lists everything worth backing up, where it lives, and how critical it is.

## Quick checklist

A reasonable "everything that matters" backup tarball:

```
peersh-backup/
├── local/                                           # PSK + Firebase secrets used by peershd
├── app/lib/firebase_options.dart                    # FlutterFire-generated Firebase config
├── app/firebase.json                                # FlutterFire config flavour
├── app/android/app/google-services.json             # Android Firebase config
├── app/ios/Runner/GoogleService-Info.plist          # iOS Firebase config (when present)
├── app/android/key.properties                       # Android release-signing keystore pointer
├── app/android/release.keystore                     # Android upload keystore
├── firebase/.firebaserc                             # Firebase project id pointer
├── ~/.android/debug.keystore                        # Android debug keystore (SHA-1/256 source)
└── docs/backup-notes.md                             # Your own README — see below
```

Encrypt before storing (e.g. `age` / `gpg` / a password manager attachment); these files contain credentials.

## Per-file detail

### `local/`

This directory is `.gitignore`d wholesale. Common contents:

| Path | What it is | Re-generate? | Backup priority |
|---|---|---|---|
| `local/<user>.psk` | Hex-encoded PSK secret you generated via `peersh-signaling psk add`. | Re-create with `psk add` and re-distribute to the user; previously-active mobile clients then need to update their server entry. | **High** — losing this kicks every PSK user off until they re-pair. |
| `local/peershd-sa.json` | Firebase service-account JSON used by the SA-JSON peershd auth flow. | Yes, via Firebase Console / `gcloud iam service-accounts keys create`. | Medium — only matters if you use the SA-JSON flow rather than `-pair-code` / `-firebase-login`. |
| `local/peershd-build.env` | ldflags inputs for the distribution build (Firebase API key, project id, OAuth client id/secret, etc.). | Yes from Firebase Console + GCP Console, but tedious. | Medium. |
| `local/firebase-refresh-token.txt` | peershd's persisted Firebase refresh token (alternate location is `%LOCALAPPDATA%\peersh\firebase-refresh-token.txt`). | Yes via `peershd -firebase-login` / `-pair-code`. | Low — re-bootstrap is one command. |
| `local/firebase_metrics_token.txt` | The bearer token your Cloud Run signaling service expects on `/metrics`. Mirror of `PEERSH_SIGNALING_METRICS_TOKEN`. | Re-generate with `openssl rand -hex 32` and `gcloud run services update`. | Low. |

### Mobile FlutterFire config (per Firebase project)

Generated once per project by `flutterfire configure`. Each is normally git-ignored (or a stub is committed and the real file kept under `git update-index --skip-worktree`).

| Path | Re-generate? | Backup priority |
|---|---|---|
| `app/lib/firebase_options.dart` | Yes via `flutterfire configure --project=<id>`. | Medium — speeds up new-machine setup. |
| `app/firebase.json` | Same as above. | Medium. |
| `app/android/app/google-services.json` | Same as above. | Medium. |
| `app/ios/Runner/GoogleService-Info.plist` | Same as above (only present after iOS configure). | Medium. |
| `firebase/.firebaserc` | Trivial — single line with `"default": "<project-id>"`. | Low. |

> `flutterfire configure` updates each of these in lockstep. If you back up only one, the others may get out of sync after re-running the CLI.

### Android signing — Release keystore

For Play Store / signed-release distribution. Each signing key has a unique SHA-1 / SHA-256 fingerprint that Firebase / OAuth / App Check need to know about.

| Path | What it is | Re-generate? | Backup priority |
|---|---|---|---|
| `app/android/key.properties` | Tells Gradle where the keystore lives + the alias / passwords. Local-only (gitignored). | Trivial; rewrite by hand from `key.properties.example`. | Low. |
| `app/android/release.keystore` | The actual upload key. **Cannot be regenerated** without rotating the Play Store upload key. Lose this and every previously-published APK becomes orphaned (no upgrade path on existing devices). | **No.** | **Critical** if you've published anything. |
| The keystore's `storePassword` / `keyPassword` | Plaintext credentials in `key.properties`. Gradle can't unlock the keystore without them. | No. | **Critical** — keep them in a password manager beside the keystore. |

> Always back up `release.keystore` + its passwords together; either one without the other is useless.

### Android signing — Debug keystore (per machine)

The default debug keystore lives at:

- Linux / macOS: `~/.android/debug.keystore`
- Windows: `%USERPROFILE%\.android\debug.keystore`

The SHA-1 of this file is what Firebase Authentication uses to authorise Google Sign-In on debug builds, and what App Check trusts for the `Debug` provider.

| Path | Re-generate? | Backup priority |
|---|---|---|
| `~/.android/debug.keystore` | Re-generated automatically by Android Studio / `flutter build` if missing. **However** the new keystore has a different SHA-1, so you must re-register it in Firebase Console under the Android app's "SHA certificate fingerprints" before Google Sign-In stops failing with `ApiException: 10`. | Medium — back up to skip the Firebase re-registration step on new machines. |

### Required SHA fingerprints to record

Whether you're moving to a new dev machine or restoring after a reinstall, write these down in your password manager or `docs/backup-notes.md`:

- Debug keystore SHA-1
- Debug keystore SHA-256
- Release keystore SHA-1
- Release keystore SHA-256

You can recover them at any time via:

```sh
cd app/android
./gradlew signingReport
```

But that requires the keystore file. If the keystore is gone, the fingerprints are gone too.

### `peershd` self-update repo + version

Embedded into the binary at build time via `embeddedUpdateRepo` + `embeddedVersion`. Not a secret — it's the GitHub `owner/repo` your binaries auto-update from. Worth recording in your build env file:

```sh
PEERSH_BUILD_UPDATE_REPO=your-github-user/peersh
PEERSH_BUILD_VERSION=v0.1.0
```

### `windows/dev-cert.pem` / `dev-key.pem`

Self-signed dev TLS certs auto-generated by `peershd` on first run under `%LOCALAPPDATA%\peersh\dev\`. The matching device id (`device_id = base32(sha256(publicKey)[:10])`) is what mobile clients use to address the host. **If you delete this directory, every mobile client's "Target device id" needs updating.**

| Path (Windows) | What it is | Re-generate? | Backup priority |
|---|---|---|---|
| `%LOCALAPPDATA%\peersh\dev\dev-cert.pem` | Dev TLS cert (self-signed, ed25519). | Yes, but the device id changes with it. | Medium — backing up keeps the device id stable across reinstalls. |
| `%LOCALAPPDATA%\peersh\dev\dev-key.pem` | Matching private key. | Same. | Same. |
| `%LOCALAPPDATA%\peersh\firebase-refresh-token.txt` | Firebase refresh token (after `-firebase-login` or `-pair-code`). | Yes via re-bootstrap. | Low. |

## Recommended backup workflow

### Generate `local/backup-notes.md` automatically

Run the helper:

```sh
bash scripts/generate-backup-notes.sh         # macOS / Linux / Git Bash
scripts\generate-backup-notes.cmd             # Windows (wraps the bash script)
```

The script reads everything it can from on-disk files and emits `local/backup-notes.md` with the values filled in. `<unset>` markers indicate fields the script couldn't auto-detect (e.g. GCP billing account, Cloud Run URL when not embedded into `local/peershd-build.env`); fill those in by hand.

What it auto-detects when the source files are present:

| Source file | Pulled into backup notes |
|---|---|
| `firebase/.firebaserc` | Firebase project id |
| `app/lib/firebase_options.dart` | Web API key |
| `app/android/app/google-services.json` | Project number, package name |
| `local/peershd-build.env` | Project id (fallback), region, signaling URL, OAuth client id/secret, embedded version + update repo |
| `local/firebase_metrics_token.txt` | Cloud Run metrics token |
| `~/.android/debug.keystore` | Debug keystore SHA-1 / SHA-256 (via `keytool`) |
| `app/android/key.properties` + release keystore | Release keystore SHA-1 / SHA-256 |

Re-run after rotating a secret to refresh. Then drop `local/backup-notes.md` (and the keystores / secret files it references) into the same encrypted backup tarball.

### Periodic backup (when you change anything)

Anytime you regenerate a secret (rotate a PSK, rotate the Firebase service-account key, replace the release keystore), refresh the encrypted backup. There's no automation today; it's a manual operator task.

### Restore on a new machine

1. Decrypt the backup tarball into a working directory.
2. Copy `local/`, `app/android/key.properties`, `app/android/release.keystore`, `firebase/.firebaserc`, and the FlutterFire config files (or run `flutterfire configure` again — the project's already registered, you just need its config locally).
3. Restore `~/.android/debug.keystore` if you want the same SHA-1 the Firebase project already trusts; otherwise generate a fresh one and add its SHA-1 / SHA-256 to Firebase Console → Project settings → Your apps → Android → SHA certificate fingerprints.
4. `git update-index --skip-worktree app/lib/firebase_options.dart app/firebase.json app/android/app/google-services.json` to keep the real values out of `git diff`.
5. Run `flutter pub get`, `bash scripts/build-mobile-core.sh android`, `flutter build apk --debug` (or `--release`) to verify the restore worked.
6. On a peershd host: drop `%LOCALAPPDATA%\peersh\dev\` from the backup; the host should start with the same device id.

## What you do NOT need to back up

- The repository itself (it's on GitHub).
- `app/build/`, `app/.dart_tool/`, `app/android/app/libs/peersh.aar`, `app/ios/Frameworks/peersh.xcframework/` — all rebuilt by `scripts/build-mobile-core.sh` and `flutter build`.
- `peersh-signaling.db` on a Cloud Run / ephemeral host (use `PEERSH_SIGNALING_BOOTSTRAP_PSK` to repopulate). On a real-disk host where SQLite is the source of truth, **do** back it up.
- Firestore data — use `gcloud firestore export` for a full export if you care about session / device history; the schema is rebuilt as devices reconnect anyway.
