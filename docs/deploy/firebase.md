# peersh Firebase mode (Phase 5b)

Firebase mode replaces the PSK / SQLite self-hosting path with Google sign-in + Firestore + FCM wake-up. It is the recommended path for an "official-hosted" peersh-signaling deployment serving more than a handful of users.

The PSK + SQLite path remains supported for self-hosters with a single VPS — pick whichever fits.

## What ships

- **Server side** (committed to the repo, generic):
  - `core/auth/firebase` — Firebase Admin SDK ID-token verifier.
  - `core/store/firestore` — Firestore-backed Store.
  - `firebase/firestore.rules` — per-user isolation.
  - `firebase/firestore.indexes.json` — composite indexes (none yet).
  - `firebase/functions/src/index.ts` — `onSessionCreated` Cloud Function that fires FCM wake-up to the matching host.
  - `peersh-signaling` config switches: `auth_provider = "firebase"`, `store_backend = "firestore"`.
- **Mobile side** (committed):
  - `firebase_core`, `firebase_auth`, `cloud_firestore`, `firebase_messaging`, `firebase_app_check`, `google_sign_in` packages in `pubspec.yaml`.
  - `app/lib/services/auth_service_firebase.dart` — Google sign-in + ID token resolver.
  - `app/lib/services/flavor.dart` — `kFirebaseEnabled = bool.fromEnvironment('PEERSH_FIREBASE')`.
  - `app/lib/screens/signin_screen.dart` — Google sign-in screen surfaced by `_FirebaseGate`.
  - `app/lib/firebase_options.dart` — **stub** with throwing `currentPlatform`. Operators run `flutterfire configure` locally to overwrite it with their project's real values.
- **peershd side** (committed):
  - `windows/firebase` — `AuthSource` mints custom tokens via the Admin SDK + exchanges them for ID tokens via the Identity Toolkit REST endpoint.
  - `peershd` flags: `-firebase-project`, `-firebase-credentials`, `-firebase-uid`, `-firebase-api-key`.

What is **not** in the repo (operator-specific, generated locally):

- Your Firebase / GCP project id.
- The `app/lib/firebase_options.dart` real values (the stub is committed; running `flutterfire configure` overwrites locally).
- `app/android/app/google-services.json` — gitignored.
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

### 5. Enable Google sign-in

This step requires a browser (Firebase Console).

1. Open `https://console.firebase.google.com/project/<your-project-id>/authentication/providers`.
2. Click **Get started** if Authentication is uninitialised.
3. Click **Google** → toggle **Enable**.
4. Set **Project public name** = your app name (e.g. `peersh`).
5. Set **Project support email** = the email you want surfaced in the sign-in dialog.
6. Save.

### 6. Deploy Firestore rules + onSessionCreated function

```sh
cp firebase/.firebaserc.example firebase/.firebaserc
$EDITOR firebase/.firebaserc                       # set "default": "<your-project-id>"

cd firebase/functions
npm install
cd ../..
```

Update the function's region to match your Firestore region (open `firebase/functions/src/index.ts` and edit the `setGlobalOptions({region: ...})` call). Then:

```sh
cd firebase
firebase deploy --only firestore:rules,firestore:indexes,functions
cd ..
```

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
```

To re-track later (e.g. when the stub format changes upstream):

```sh
git update-index --no-skip-worktree app/lib/firebase_options.dart
```

`google-services.json` is in `.gitignore`, so no `--skip-worktree` is needed for it.

### 2. Build the Firebase-enabled APK

The Android Gradle build conditionally applies `com.google.gms.google-services` based on the `PEERSH_FIREBASE` environment variable; the Dart side reads `kFirebaseEnabled = bool.fromEnvironment('PEERSH_FIREBASE')`. Both gates need to be set to `true`:

```sh
# Bash / Git Bash:
PEERSH_FIREBASE=true flutter build apk --debug --dart-define=PEERSH_FIREBASE=true

# Windows cmd:
set PEERSH_FIREBASE=true && flutter build apk --debug --dart-define=PEERSH_FIREBASE=true

# PowerShell:
$env:PEERSH_FIREBASE = "true"; flutter build apk --debug --dart-define=PEERSH_FIREBASE=true
```

The default build (no env var, no dart-define) skips both layers and produces a PSK-only APK that does not require `google-services.json`.

### 3. Install + sign in

```sh
adb install -r app/build/app/outputs/flutter-apk/app-debug.apk
```

Open the app → **Sign in with Google** → pick the Google account you want associated with this peersh installation. The app stores the Firebase ID token internally and uses it on every signaling Register frame.

After sign-in completes, Firebase Console → **Authentication → Users** lists the freshly-created uid. Note this uid; peershd needs it.

## peershd — Firebase auth source

peershd registers with the signaling server using a fresh Firebase ID token minted from a service-account JSON. The JSON file stays on the host's disk (do not commit it).

### 1. Create the service account

```sh
gcloud iam service-accounts create peershd-host \
  --display-name="peershd Firebase auth source" \
  --project=<your-project-id>

gcloud projects add-iam-policy-binding <your-project-id> \
  --member="serviceAccount:peershd-host@<your-project-id>.iam.gserviceaccount.com" \
  --role="roles/firebaseauth.admin"
```

### 2. Generate a key

If your org enforces `constraints/iam.disableServiceAccountKeyCreation` (most do by default), you'll see a "Key creation is not allowed on this service account" error. Override at the project level:

```sh
echo "constraint: constraints/iam.disableServiceAccountKeyCreation
booleanPolicy:
  enforced: false" > /tmp/policy.yaml
gcloud resource-manager org-policies set-policy /tmp/policy.yaml --project=<your-project-id>

# Wait ~30 seconds for propagation, then:
gcloud iam service-accounts keys create local/peershd-sa.json \
  --iam-account=peershd-host@<your-project-id>.iam.gserviceaccount.com \
  --project=<your-project-id>
```

`local/` is gitignored. Keep `peershd-sa.json` there.

### 3. Find the Firebase Web API key

```sh
# From the FlutterFire-generated file (locally):
grep "apiKey" app/lib/firebase_options.dart | head -1
```

Or open Firebase Console → **Project settings → General → Your apps → Web SDK snippet → apiKey**.

### 4. Run peershd in Firebase mode

```sh
peershd \
  -signaling wss://<assigned-host>/ws \
  -firebase-project <your-project-id> \
  -firebase-credentials local/peershd-sa.json \
  -firebase-uid <uid-from-step-mobile-3> \
  -firebase-api-key <api-key-from-step-3> \
  -display-name "<your-host-name>"
```

`-firebase-uid` MUST be the same uid the mobile app signed in as — both peershd and the mobile client must land in the same `users/{uid}/...` Firestore namespace for routing to work.

peershd logs `registered with signaling server (firebase mode)` on success and `device_id=<id>` for the value the mobile client uses as the connection target.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `auth: firebase: VerifyIDToken: ...` on Register | The ID token's project does not match `PEERSH_SIGNALING_FIREBASE_PROJECT_ID`. Make sure the mobile app + peershd + signaling server all point at the same Firebase project. |
| Mobile app: `signaling: register rejected by server: auth: firebase: ...` | Either Firestore rules failed, or the user's uid is not allowed (rules are per-uid; the bug is usually a peershd uid mismatch). |
| peershd: `firebase: identity toolkit 400: API key not valid` | Check `-firebase-api-key`. The Web SDK key is the right one — not the Android-only OAuth client id. |
| Mobile sign-in: `PlatformException(sign_in_failed, ApiException: 10)` | The Google sign-in OAuth client has not been auto-configured. Re-run `flutterfire configure --platforms=android` to register the Android app's SHA-1. |
| `Permission denied while using the Eventarc Service Agent` (Cloud Function deploy) | Wait ~60 s; first 2nd-gen function in a fresh project takes a moment for the service agent. |

## Cost expectations

For personal-scale use (a few users, a few peershd hosts), Firebase mode stays comfortably within the free tier:

- Firestore: < 1.5M reads/month, < 600k writes/month free.
- Cloud Functions: 2M invocations/month free.
- FCM: free regardless of volume.
- Cloud Run: 180k vCPU-seconds + 2M requests/month free.

In Firebase mode, peershd does **not** keep the signaling WebSocket open continuously — it only connects long enough to reply to a Connect message after FCM wake-up, then disconnects again. This is dramatically cheaper than the PSK mode where peershd's WebSocket stays open all day.

At ~1000 users with average traffic, expect total monthly cost in the low single-digit dollars across Cloud Run + Firestore + Functions; the dominant variable is FCM usage and that is free.
