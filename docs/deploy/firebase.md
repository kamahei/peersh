# peersh Firebase mode

Firebase mode replaces the PSK / SQLite self-hosting path with Google sign-in + Firestore (device / pairing state) + Realtime Database (wake events). It is the recommended path for an "official-hosted" peersh-signaling deployment serving more than a handful of users. The signaling WebSocket opens only briefly per session — host-side wake-up flows over a Realtime Database SSE stream that does not contribute to Cloud Run billing. See [`docs/firebase-mode.md`](../firebase-mode.md) for the runtime architecture.

The PSK + SQLite path remains supported for self-hosters with a single VPS — pick whichever fits.

## What ships

- **Server side** (committed to the repo, generic):
  - `core/auth/firebase` — Firebase Admin SDK ID-token verifier.
  - `core/store/firestore` — Firestore-backed Store (devices / pairings / users).
  - `firebase/firestore.rules` — per-user isolation.
  - `firebase/firestore.indexes.json` — composite indexes (none yet).
  - `firebase/database.rules.json` — Realtime Database per-user isolation rules (`users/{uid}/...`).
  - `firebase/functions/src/index.ts` — `mintPairingCode` / `claimPairingCode` for the headless pairing flow, plus `budgetGuard`. The legacy `onSessionCreated` FCM wake-up is retained as dead code (a future v2-D may revive it).
  - `peersh-signaling` config switches: `auth_provider = "firebase"`, `store_backend = "firestore"`, `idle_timeout = "60s"` (defense layer for stale connections).
- **Mobile side** (committed):
  - `firebase_core`, `firebase_auth`, `cloud_firestore`, `firebase_database`, `firebase_messaging`, `firebase_app_check`, `google_sign_in` packages in `pubspec.yaml`.
  - `app/lib/services/auth_service_firebase.dart` — Google sign-in + ID token resolver.
  - `app/lib/services/rtdb.dart` — RTDB instance helper (region is hard-coded; edit if your project uses a non-`asia-southeast1` region).
  - `app/lib/services/peersh_session.dart` — writes wake_request to RTDB on each connect attempt; reads `last_seen_at` for the presence freshness hint.
  - `app/lib/services/device_discovery_service.dart` — reads `users/{uid}/devices` from RTDB to populate the device picker.
  - `app/lib/screens/signin_screen.dart` — Google sign-in screen, surfaced from the connect flow when the user opens a Firebase server entry.
  - `app/lib/screens/pair_pc_screen.dart` — **Pair PC** screen: shows a 6-digit code peershd consumes once to bootstrap.
  - `app/lib/services/pairing_service.dart` — calls the `mintPairingCode` Cloud Function with the user's Firebase ID token.
  - `app/lib/firebase_options.dart`, `app/firebase.json`, `app/android/app/google-services.json` — **stubs** committed; operators run `flutterfire configure` locally (or use `scripts/build-apk-distrib.{sh,cmd}` which swaps in `local/*.real` at build time and restores the stubs after).
- **Cloud Functions** (committed in `firebase/functions/src/index.ts`):
  - `mintPairingCode` — HTTPS, ID-token-authenticated. Mints a Custom Token for the calling uid, stores it under `pairing_codes/{code}` with a 5-min TTL, and returns the 6-digit code to the mobile app.
  - `claimPairingCode` — HTTPS, unauthenticated. Takes a code, returns the cached Custom Token, deletes the doc.
  - `budgetGuard` — Pub/Sub subscriber that flips `ops/budget-state.triggered` when a Cloud Billing alert fires; retained for a future server-side wake-throttling path.
  - `onSessionCreated` — present but **dead code** (no client writes `users/{uid}/sessions/{sid}` after v2-A; wake events flow via RTDB instead).
- **peershd side** (committed):
  - `windows/firebase` — `RefreshAuthSource` (pairing-code / browser-login) and `AuthSource` (service-account JSON), plus the RTDB SSE wake-listener (`rtdb.go`, `wakelistener.go`, `devices.go`). Both `TokenSource` implementations feed the same wake-listener path.
  - `peershd` flags: `-firebase-login`, `-pair-code`, `-firebase-project`, `-firebase-api-key`, `-firebase-region` (Cloud Functions region), `-firebase-rtdb-region` (RTDB region; default `asia-southeast1`), `-firebase-token-file`, `-firebase-credentials`, `-firebase-email`, `-firebase-uid`.

What is **not** in the repo (operator-specific, generated locally):

- Your Firebase / GCP project id.
- The `app/lib/firebase_options.dart` real values (the stub is committed; running `flutterfire configure` overwrites locally).
- The `app/android/app/google-services.json` real values (a buildable stub is committed).
- `app/ios/Runner/GoogleService-Info.plist` — gitignored.
- `firebase/.firebaserc` — gitignored. Use `firebase/.firebaserc.example` as a template.
- Service-account JSON for peershd — keep under `local/` (already in `.gitignore`).
- The Firebase Web API key — get it from Firebase console → Project settings → General → Web app.

## End-to-end operator setup

Skip steps 1-3 if you already have a Firebase / GCP project and just want to wire peersh up to it.

### 1. Create the project

```sh
gcloud projects create <your-project-id> --name="<your-project-id>"

# Link billing (required even for free-tier-only use)
gcloud beta billing accounts list
gcloud beta billing projects link <your-project-id> --billing-account=<XXXXXX-XXXXXX-XXXXXX>

gcloud config set project <your-project-id>
```

### 2. Enable APIs

```sh
gcloud services enable \
  run.googleapis.com \
  cloudbuild.googleapis.com \
  artifactregistry.googleapis.com \
  firebase.googleapis.com \
  firestore.googleapis.com \
  firebasedatabase.googleapis.com \
  identitytoolkit.googleapis.com \
  fcm.googleapis.com \
  cloudfunctions.googleapis.com \
  cloudresourcemanager.googleapis.com \
  --project=<your-project-id>
```

### 3. Add Firebase to the GCP project

```sh
firebase login        # one-time, opens a browser
firebase projects:addfirebase <your-project-id>
```

### 4. Initialize Firestore (Native mode)

```sh
gcloud firestore databases create \
  --location=<region> \
  --type=firestore-native \
  --project=<your-project-id>
```

`<region>` recommendations:

- `asia-northeast1` (Tokyo) — best latency from Japan; Cloud Run tier 2 (~30 % more expensive than tier 1, but the absolute difference is small at low scale).
- `us-central1` (Iowa) — cheapest Cloud Run + Firestore.
- `asia-east1` (Taiwan) — middle ground: Cloud Run tier 1 pricing, ~60 ms RTT from Japan.

Pick one and use it consistently for Firestore + Cloud Run + Cloud Functions to avoid cross-region egress fees.

### 4a. Initialize Realtime Database (one-time)

Wake events for v2-A live in the Realtime Database, not Firestore. The Firebase RTDB CLI does not support non-interactive instance creation; do this from the console:

1. Open `https://console.firebase.google.com/project/<your-project-id>/database`.
2. **Realtime Database** → **Create Database**.
3. **Region**: pick `asia-southeast1` (Singapore), `europe-west1`, or `us-central1`. **`asia-northeast1` is NOT supported by RTDB.** The mobile app hard-codes `asia-southeast1` in `app/lib/services/rtdb.dart` — if you choose a different region, edit that constant before building the APK.
4. Start in **Locked mode**. The rules are uploaded by `firebase deploy --only database` in step 6 below.

### 5. Enable Google sign-in

This step requires a browser (Firebase Console).

1. Open `https://console.firebase.google.com/project/<your-project-id>/authentication/providers`.
2. Click **Get started** if Authentication is uninitialised.
3. Click **Google** → toggle **Enable**.
4. Set **Project public name** = your app name (e.g. `peersh`).
5. Set **Project support email** = the email you want surfaced in the sign-in dialog.
6. Save.

### 6. Deploy Firestore rules + Realtime Database rules + Cloud Functions

```sh
cp firebase/.firebaserc.example firebase/.firebaserc
$EDITOR firebase/.firebaserc                       # set "default": "<your-project-id>"

cd firebase/functions
npm install
cd ../..
```

Update the function's region to match your Cloud Functions region (open `firebase/functions/src/index.ts` and edit the `setGlobalOptions({region: ...})` call). Then:

```sh
cd firebase
firebase deploy --only firestore:rules,firestore:indexes,database,functions
cd ..
```

The `database` target uploads `firebase/database.rules.json`, which restricts `users/{uid}/...` (wake_requests + devices presence) read/write to the matching authenticated uid.

If the first `functions` deploy fails with a "Permission denied while using the Eventarc Service Agent" error, the Eventarc service agent has not finished propagating yet. Wait ~60 s and retry; the error is transient and only affects the very first 2nd-gen function in a fresh project. The Cloud Build worker also needs `roles/cloudbuild.builds.builder` granted to the Compute default service account; if you see "missing required permission on the build service account":

```sh
PROJECT_NUMBER=$(gcloud projects describe <your-project-id> --format='value(projectNumber)')
gcloud projects add-iam-policy-binding <your-project-id> \
  --member="serviceAccount:${PROJECT_NUMBER}-compute@developer.gserviceaccount.com" \
  --role="roles/cloudbuild.builds.builder"
```

### 7. Deploy peersh-signaling to Cloud Run in Firebase mode

```sh
PROJECT_ID=<your-project-id> REGION=<region> \
  bash server/deploy/cloud-run/deploy.sh
```

> **Windows / PowerShell tip.** Invoking `bash` from PowerShell with a backslash path strips the backslashes (`bash D:\peersh\...\deploy.sh` → `D:peersh...deploy.sh: No such file or directory`). Either single-quote the path (`bash 'D:\peersh\server\deploy\cloud-run\deploy.sh'`) or use forward slashes (`bash D:/peersh/server/deploy/cloud-run/deploy.sh`). Or `cd` into the repo root first and use a relative path.

Then set the environment variables that switch the running service into Firebase mode (the deploy script's defaults are PSK-friendly; Firebase mode adds two more):

```sh
METRICS_TOKEN=$(openssl rand -hex 32)
gcloud run services update peersh-signaling \
  --region=<region> \
  --project=<your-project-id> \
  --update-env-vars=PEERSH_SIGNALING_AUTH_PROVIDER=firebase,\
PEERSH_SIGNALING_STORE_BACKEND=firestore,\
PEERSH_SIGNALING_FIREBASE_PROJECT_ID=<your-project-id>,\
PEERSH_SIGNALING_DISCOVERY_WS_URL=wss://<assigned-host>/ws,\
PEERSH_SIGNALING_METRICS_TOKEN=$METRICS_TOKEN
```

Replace `<assigned-host>` with the hostname the deploy script printed.

Optional but recommended: set `PEERSH_SIGNALING_IDLE_TIMEOUT` to override the default 60-second idle close (defense layer against frozen clients holding the WS open):

```sh
gcloud run services update peersh-signaling \
  --region=<region> --project=<your-project-id> \
  --update-env-vars=PEERSH_SIGNALING_IDLE_TIMEOUT=120s
```

Use `-1s` to disable idle close entirely (not recommended for production).

The Cloud Run runtime service account needs Firestore write access — the project's Compute default SA usually has it via `roles/datastore.user`. If not:

```sh
gcloud projects add-iam-policy-binding <your-project-id> \
  --member="serviceAccount:${PROJECT_NUMBER}-compute@developer.gserviceaccount.com" \
  --role="roles/datastore.user"
```

If the org you're under enforces the `iam.allowedPolicyMemberDomains` policy, `--allow-unauthenticated` will not actually grant `allUsers` access — you'll get 403s on every request. Override per project via the Cloud Run console (**Security** → **Allow public access**) or, if you have `roles/orgpolicy.policyAdmin`, via:

```sh
echo "constraint: constraints/iam.allowedPolicyMemberDomains
listPolicy:
  allValues: ALLOW" > /tmp/policy.yaml
gcloud resource-manager org-policies set-policy /tmp/policy.yaml --project=<your-project-id>
```

### 8. Verify the signaling endpoints

```sh
URL=https://<assigned-host>
curl -sS $URL/health                                  # → ok
curl -sS $URL/.well-known/peersh.json                 # → "auth_providers": ["firebase"]
curl -sS -H "Authorization: Bearer $METRICS_TOKEN" \
     $URL/metrics | head -3                           # → Prometheus exposition
```

## Mobile app — turn on Firebase mode

### 1. Generate FlutterFire config

```sh
dart pub global activate flutterfire_cli       # one-time
cd app
flutterfire configure --project=<your-project-id> --platforms=android
```

This writes:

- `app/lib/firebase_options.dart` (overwriting the committed stub with real values for **your** project).
- `app/android/app/google-services.json`.
- `app/ios/Runner/GoogleService-Info.plist` (only if `--platforms=ios` is passed; macOS-only build).

To prevent your project values from showing up in `git diff` (they're operator-specific, not part of the OSS source tree):

```sh
git update-index --skip-worktree app/lib/firebase_options.dart
git update-index --skip-worktree app/android/app/google-services.json
git update-index --skip-worktree app/firebase.json
```

To re-track later (e.g. when the stub format changes upstream):

```sh
git update-index --no-skip-worktree app/lib/firebase_options.dart
git update-index --no-skip-worktree app/android/app/google-services.json
git update-index --no-skip-worktree app/firebase.json
```

The Android and Dart Firebase config stubs are committed so the default OSS build still works. Use `skip-worktree` after replacing them locally with your project values.

### 2. Build the APK

The same APK supports both PSK and Firebase server entries; per-server `authMode` selects the auth path at connect time.

```sh
cd app
flutter build apk --debug
```

The build always applies the `com.google.gms.google-services` Gradle plugin and includes the Firebase Auth + Firestore + Messaging packages. At runtime, `Firebase.initializeApp` is called with try/catch — if `firebase_options.dart` is the OSS stub (no real project values), initialization fails and the app silently runs in PSK-only mode. After `flutterfire configure`, initialization succeeds and Firebase server entries become functional.

### 3. Install + sign in + pair

```sh
adb install -r app/build/app/outputs/flutter-apk/app-debug.apk
```

1. Open the app → **Settings** → **Pair PC**.
2. Tap **Generate code**. The app prompts for Google sign-in if you haven't signed in yet, then displays a 6-digit pairing code.
3. On the PC side run peershd with `-pair-code <that code>` (see the next section). The code expires after 5 minutes and is consumed on first claim.

You can now add a Firebase-mode server entry in the app: tap **Add server**, set **Auth mode = Firebase**, paste the signaling `wss://` URL and the device id peershd printed at startup.

## peershd — Firebase auth source

peershd registers with the signaling server using a fresh Firebase ID token. There are three bootstrap paths, in increasing order of complexity / friction:

1. **Browser sign-in (`-firebase-login`)** — recommended on a desktop with a browser. peershd opens Google's sign-in page in your default browser, then persists a refresh token scoped to that uid. No code typing.
2. **Pairing code (`-pair-code`)** — required on headless / kiosk hosts (no browser). The mobile app generates a one-time 6-digit code that peershd consumes once.
3. **Service-account JSON (`-firebase-credentials`)** — multi-host fleet provisioning. Project-wide credential, kept under an advanced section below.

> **Tip — distribution build.** If you're shipping peershd binaries to other people (family, team, or as part of a paid product), bake your project's defaults into the binary so end users don't need to pass `-firebase-*` / `-google-*` flags. See `docs/deploy/self-hosting.md` for the `scripts/build-peershd-distrib.sh` walkthrough; the rest of this section assumes the operator (you) is running peershd directly with explicit flags.

### 1. Find the Firebase Web API key

```sh
# From the FlutterFire-generated file (locally):
grep "apiKey" app/lib/firebase_options.dart | head -1
```

Or open Firebase Console → **Project settings → General → Your apps → Web SDK snippet → apiKey**. The Web API key is *public by design* (it identifies the project, not the caller); it's safe to embed it in any peershd invocation.

### 2a. Browser sign-in (recommended on desktops)

Create an OAuth 2.0 "Desktop app" client one time in [Google Cloud Console → APIs & Services → Credentials → Create Credentials → OAuth client ID](https://console.cloud.google.com/apis/credentials). Pick **Desktop app**, name it "peersh-cli", and note the generated client id + client secret. (Google's docs explicitly state the secret of an Installed App client "isn't actually treated as a secret", so embedding it in operator-side scripts or in `peershd.exe` invocations is fine.)

Then on the PC:

```sh
peershd \
  -signaling wss://<assigned-host>/ws \
  -firebase-project <your-project-id> \
  -firebase-api-key <api-key-from-step-1> \
  -firebase-login \
  -google-client-id <oauth-client-id> \
  -google-client-secret <oauth-client-secret> \
  -display-name "<your-host-name>"
```

peershd opens the default browser at Google's sign-in page; pick the same Google account the mobile app uses. After consent the browser shows a "Sign-in successful" page and peershd writes a refresh token to `%LOCALAPPDATA%\peersh\firebase-refresh-token.txt`.

### 2b. Pairing code (works on headless hosts)

Tap **Settings → Pair PC** in the mobile app and **Generate code**. With that code in hand, on the PC:

```sh
peershd \
  -signaling wss://<assigned-host>/ws \
  -firebase-project <your-project-id> \
  -firebase-api-key <api-key-from-step-1> \
  -firebase-region <region> \
  -pair-code <6-digit-code> \
  -display-name "<your-host-name>"
```

On success peershd writes the refresh token to `%LOCALAPPDATA%\peersh\firebase-refresh-token.txt` (override with `-firebase-token-file`) and logs `registered with signaling server (firebase mode)` plus `device_id=<id>`. The 6-digit code is consumed on first claim and cannot be reused.

### 3. Subsequent runs

Drop both `-firebase-login` and `-pair-code` — peershd loads the persisted refresh token and uses it to mint fresh ID tokens on demand:

```sh
peershd \
  -signaling wss://<assigned-host>/ws \
  -firebase-project <your-project-id> \
  -firebase-api-key <api-key-from-step-1> \
  -display-name "<your-host-name>"
```

If the refresh token is lost (file deleted, machine reset) or revoked (mobile app **Sign out** + sign back in), re-run with `-firebase-login` or `-pair-code` to bootstrap again. There's no per-uid quota.

> **RTDB region.** peershd's wake-listener targets `<project>-default-rtdb.<region>.firebasedatabase.app` where `<region>` defaults to `asia-southeast1` (Singapore). If your RTDB instance lives elsewhere, pass `-firebase-rtdb-region <region>` (or set `PEERSH_BUILD_FIREBASE_RTDB_REGION` at build time so the binary embeds it). The same value must match the hard-coded `_rtdbRegion` in `app/lib/services/rtdb.dart` on the mobile side.

### Optional: peershd Prometheus /metrics

peershd ships a Prometheus exposition endpoint that defaults to `127.0.0.1:9101/metrics` — operator-only, no firewall surface. Useful for confirming wake-event delivery latency, heartbeat failures, and SSE listener reconnects without grepping logs. See [`docs/firebase-mode.md`](../firebase-mode.md#observability) for the full metric inventory and example PromQL.

To scrape from a remote Prometheus, bind to a non-loopback address and set a bearer token:

```sh
peershd \
  -firebase-project <your-project-id> \
  -firebase-api-key <api-key> \
  -metrics-addr 0.0.0.0:9101 \
  -metrics-token <bearer-token-or-set-PEERSH_METRICS_TOKEN-env>
```

peershd refuses to start when `-metrics-addr` is non-loopback and the token is empty, mirroring the `PEERSH_SIGNALING_METRICS_TOKEN` fail-closed contract on the signaling server.

### Advanced: service-account JSON path

For multi-host deployments where a single operator wants to provision dozens of peershds without each one touching the mobile app, peershd still accepts the service-account-JSON flow:

```sh
gcloud iam service-accounts create peershd-host \
  --display-name="peershd Firebase auth source" \
  --project=<your-project-id>

gcloud projects add-iam-policy-binding <your-project-id> \
  --member="serviceAccount:peershd-host@<your-project-id>.iam.gserviceaccount.com" \
  --role="roles/firebaseauth.admin"
```

If your org enforces `constraints/iam.disableServiceAccountKeyCreation` (most do by default), override at the project level first via `gcloud resource-manager org-policies set-policy`. Then:

```sh
gcloud iam service-accounts keys create local/peershd-sa.json \
  --iam-account=peershd-host@<your-project-id>.iam.gserviceaccount.com \
  --project=<your-project-id>

peershd \
  -signaling wss://<assigned-host>/ws \
  -firebase-project <your-project-id> \
  -firebase-credentials local/peershd-sa.json \
  -firebase-email <your-email> \
  -firebase-api-key <api-key> \
  -display-name "<your-host-name>"
```

This grants peershd the ability to mint tokens for **any** uid in the project — keep `peershd-sa.json` strictly on trusted hosts. The pairing flow is preferred for personal use because the persisted credential is scoped to a single uid.

## App Check (anti-abuse)

Without App Check, anyone with the Firebase Web API key can mint ID tokens and Register against the signaling server. App Check blocks this: the mobile app attests its integrity (Play Integrity on Android, debug provider in dev) and the server rejects Register frames whose attestation doesn't pass.

Roll out in this order so existing clients don't break:

1. **Enable App Check in Firebase Console**
   `https://console.firebase.google.com/project/<your-project-id>/appcheck`
   Click **Get started** → register the Android app → choose **Play Integrity** as the provider. (For iOS, use **App Attest**; iOS support in peersh is deferred to a future phase.)

2. **Roll out the mobile build that sends App Check tokens.**
   The shipped APK calls `FirebaseAppCheck.instance.activate(...)` on launch and forwards the token on every Register. If App Check isn't yet enabled in the console, the token is empty/invalid — the server logs but doesn't reject (since `app_check_required = false`). Wait until your users have updated.

3. **Enforce on the signaling server.**
   Once telemetry shows healthy App Check tokens are arriving, enable enforcement:

   ```sh
   gcloud run services update peersh-signaling \
     --region=<region> \
     --project=<your-project-id> \
     --update-env-vars=PEERSH_SIGNALING_FIREBASE_APP_CHECK_REQUIRED=true
   ```

Debug builds use the App Check `Debug` provider, which only works after you register the device's debug token in Firebase Console → App Check → Manage debug tokens. Production builds use Play Integrity automatically (`kReleaseMode` switches in `app/lib/main.dart`).

## Cost guardrail

The `budgetGuard` Cloud Function listens to a Cloud Billing budget alert Pub/Sub topic. When the configured threshold is hit, it writes `ops/budget-state.triggered = true` in Firestore. After v2-A this flag is no longer consulted on the wake path (mobile writes wake_requests directly to RTDB; no Cloud Function in the loop), but the function and the flag are kept for a future server-side wake-throttling path. To kill the wake path entirely under budget pressure, the operator removes the `users/{uid}` write rule from `database.rules.json` and redeploys.

To wire it up:

```sh
# 1. Create the Pub/Sub topic the budget alert will publish to.
gcloud pubsub topics create peersh-budget-alert --project=<your-project-id>

# 2. Cloud Billing → Budgets & alerts → Create budget. Attach
#    `peersh-budget-alert` as the Pub/Sub notification topic. Set
#    thresholds (e.g. 50%, 90%, 100%).

# 3. Deploy the function (already in firebase/functions/src/index.ts).
cd firebase
firebase deploy --only functions:budgetGuard
```

When the alert fires:

```sh
# Resume operation after fixing the cost runaway:
gcloud firestore documents delete --project=<your-project-id> \
  ops/budget-state
```

You can tighten the trigger ratio via the function's `PEERSH_BUDGET_GUARD_THRESHOLD` env var (default `1.0` = 100%; set to `0.9` to stop FCM wakes at 90% of budget). Edit in `firebase.json` or via `gcloud run services update` on the underlying Cloud Run service.

## Multiple PCs

Each peershd writes its `last_seen_at` to `users/{uid}/devices/{deviceId}` in the **Realtime Database** every 5 minutes (the heartbeat) and re-asserts on each wake. The mobile app reads this subtree from RTDB and surfaces a picker:

- **First connect to a Firebase server** — the app prompts you to pick which PC to connect to. The choice is remembered as the server entry's default.
- **Switching PCs later** — long-press / tap the server's overflow menu (⋮) → **Switch PC**. The picker shows all your registered hosts ordered by most-recently-seen.

To run a second host on a different machine, just run `peershd` there with `-firebase-login` (or `-pair-code`). It writes its presence to RTDB; the picker shows it on the next refresh.

> **Note.** The signaling server still writes `display_name` / `kind` / `public_key` to the Firestore `users/{uid}/devices/{deviceId}` document on every Register frame. That data is unused by the mobile picker after v2-A but stays available for ops debugging.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `auth: firebase: VerifyIDToken: ...` on Register | The ID token's project does not match `PEERSH_SIGNALING_FIREBASE_PROJECT_ID`. Make sure the mobile app + peershd + signaling server all point at the same Firebase project. |
| Mobile app: `signaling: register rejected by server: auth: firebase: ...` | Either Firestore rules failed, or the user's uid is not allowed (rules are per-uid; the bug is usually a peershd uid mismatch). |
| peershd: `firebase: identity toolkit 400: API key not valid` | Check `-firebase-api-key`. The Web SDK key is the right one — not the Android-only OAuth client id. |
| Mobile sign-in: `PlatformException(sign_in_failed, ApiException: 10)` | The Google sign-in OAuth client has not been auto-configured. Re-run `flutterfire configure --platforms=android` to register the Android app's SHA-1. |
| `Permission denied while using the Eventarc Service Agent` (Cloud Function deploy) | Wait ~60 s; first 2nd-gen function in a fresh project takes a moment for the service agent. |
| peershd: `rtdb: PUT /users//devices/...: HTTP 401: Permission denied` | Empty uid in the URL (note the double slash). Pre-`4a25fcc` peershd called `src.UID()` before minting an ID token; the modern fix lives in `windows/firebase/devices.go::StartWakeRuntime`. Rebuild peershd from a checkout that includes commit `4a25fcc`. |
| peershd: SSE listener fails with 404 on `firebasedatabase.app` | Realtime Database instance was not created. Re-run step 4a in the Firebase Console, then `firebase deploy --only database`. |
| Mobile: `Bad state: Firebase server entry but Firebase is not initialized in this APK` | The committed `firebase_options.dart` is the OSS placeholder. Run `flutterfire configure` (or use `scripts/build-apk-distrib.{sh,cmd}`, which swaps in `local/*.real` at build time) and rebuild the APK. |
| Mobile: wake works on `asia-southeast1` but not on a different RTDB region | The mobile app hard-codes the RTDB region in `app/lib/services/rtdb.dart`. Edit `_rtdbRegion` to match your project and rebuild. |

## Cost expectations

For personal-scale use (a few users, a few peershd hosts), Firebase mode stays comfortably within the free tier:

- Firestore: < 1.5M reads/month, < 600k writes/month free (Spark plan).
- Realtime Database: 100 simultaneous connections + 1 GB stored / 10 GB transfer per month free (Spark). **Above 100 connected hosts requires the Blaze plan.**
- Cloud Functions: 2M invocations/month free.
- Cloud Run: 180k vCPU-seconds + 2M requests/month free.

In Firebase mode, peershd does **not** keep the signaling WebSocket open continuously — it holds an SSE connection to RTDB instead, which never touches Cloud Run. The WS is opened only when a wake event arrives and is closed within ~20 seconds. This is dramatically cheaper than PSK mode where peershd's WebSocket stays open all day.

At ~1000 users on the Blaze plan expect roughly:

- Cloud Run: a few dollars per month (per-session WS billing only).
- RTDB: ~$10-20/month (1000 idle SSE connections + heartbeat writes; tune via heartbeat interval if needed).
- Firestore + Functions: well within free tier.
