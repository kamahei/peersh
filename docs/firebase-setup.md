# Firebase mode for `peersh-signaling`

Phase 5 introduces Firebase as a sibling option to the PSK / SQLite self-hosting path. It exists primarily so the **official hosted** peersh server can offer Google sign-in and FCM-based wake-up. Self-hosters with a small VPS should keep using PSK + SQLite — the Firebase path costs more to operate and adds a dependency on Google's infrastructure.

The mobile-app side of Firebase (Google sign-in via FlutterFire, FCM token registration on the host) lands as a follow-up to Phase 5 once the operator side is real. This document is for operators who want to stand up the Firebase backend.

## What Phase 5 ships

- `core/auth/firebase` — server-side ID-token verification.
- `core/store/firestore` — server-side store backed by Cloud Firestore.
- `peersh-signaling` config switches: `auth_provider = "firebase"` and `store_backend = "firestore"`.
- `firebase/` — operator-side Firebase project artifacts:
  - `firestore.rules` — per-user isolation.
  - `firestore.indexes.json` — empty stub.
  - `functions/src/index.ts` — Cloud Function that fires FCM wake-up when a session document is created.

## Prerequisites

- A Google Cloud / Firebase project. Note the **project id**.
- Firestore enabled (Native mode, single region preferred for cost).
- Cloud Messaging enabled.
- Firebase Authentication enabled with at least one provider (Google, email-link, etc.).
- Firebase CLI: `npm install -g firebase-tools` (Node.js 20+).
- A service-account JSON for the signaling-server VM with the
  `Cloud Datastore User`, `Firebase Authentication Admin`, and `Cloud Messaging Admin` roles.

## Operator setup

```sh
cd firebase
cp .firebaserc.example .firebaserc
$EDITOR .firebaserc                       # set "default": "<your-project-id>"

# 1) Deploy security rules.
firebase deploy --only firestore:rules

# 2) Build & deploy the FCM Cloud Function.
cd functions
npm install
npm run build
cd ..
firebase deploy --only functions
```

On the signaling server VM:

```sh
# Install your service-account JSON, e.g. at /etc/peersh/service-account.json
# (file mode 0600, owned by the user running peersh-signaling).

# Update /etc/peersh/signaling.toml:
auth_provider = "firebase"
store_backend = "firestore"

[firebase]
project_id        = "your-project-id"
credentials_path  = "/etc/peersh/service-account.json"

# Restart the server.
systemctl restart peersh-signaling
```

The server will load the Firebase Admin SDK at startup and refuse to boot if `project_id` or the service-account file is missing.

## What the server does

- **Register**: client sends `Register{firebase_id_token=...}`. The server's firebase auth provider calls `Auth.VerifyIDToken(idToken)`. The token's `uid` becomes the peersh `user_id`. Lazy `users/{uid}` document creation in Firestore happens here.
- **Connect** routing: unchanged from Phase 2/3. Cross-user routing still rejected by `room.Forward`.
- **Session creation** (Phase 5+): when the server creates a session for a wake-required host, it writes `users/{uid}/sessions/{sessionId}`. The Cloud Function trigger fires FCM to the host's `fcm_token`.

## Cost discipline

Per `docs/architecture.md`'s cost section:

- A connection lifecycle should consume **≤ 5 reads + 2 writes**. The default Firestore store implementation is shaped to fit; verify with the Firestore usage dashboard.
- Use [Budget Alerts](https://console.cloud.google.com/billing/alerts) on the GCP billing account.
- For the Cloud Functions side, [App Engine Daily Spending Limits](https://console.cloud.google.com/appengine/settings) (Functions still inherit App Engine's quota in many GCP accounts) act as a hard cap.
- A future Phase 5/7 polish item adds a Cloud Function that consumes Pub/Sub budget-breach notifications and disables `onSessionCreated` to fail-safe.

## App Check

Phase 5 ships server-side ID-token verification but does not yet require App Check tokens. To add App Check:

1. Enable Play Integrity (Android) and App Attest (iOS) in the Firebase console.
2. Update the mobile app to attach an App Check token to every Firestore / FCM call (FlutterFire side).
3. The Admin SDK on the signaling server can call `appcheck.VerifyToken(...)` and reject Register messages that lack a valid token.

This is straightforward to wire and is a Phase 5b deliverable once the FlutterFire integration in the app is in place.

## What is NOT in Phase 5

- FlutterFire integration in the mobile app (Google sign-in, FCM token registration). The mobile app currently ships PSK-only; pulling in `firebase_core` + plugins requires a `google-services.json` per Flutter project, which is operator-specific. Phase 5b adds an opt-in Firebase build flavor.
- Full App Check enforcement (server rejects on missing/invalid token). Mocked here; the wiring is one-line once FlutterFire ships.
- Auto-disable-on-budget-breach Cloud Function (cost guardrail). Documented above; implementation lands in Phase 7 polish if real-cost data warrants.

## Trade-offs

- Operators in Firebase mode lose the ability to issue PSKs (Firestore store returns `ErrNotFound` for all PSK methods). Mixing PSK clients and Firebase clients on the same server is out of scope.
- Firebase ID tokens are short-lived (1 hour). Clients must refresh and re-register; the existing signaling client side will gain refresh handling in Phase 5b.
- Firestore charges per read/write, even at low volume. The PSK + SQLite path remains the recommended self-hosting default for cost reasons.
