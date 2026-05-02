# Migration: v1 (Firestore wake) → v2-A (Realtime Database wake)

This runbook upgrades a deployed peersh installation from the v1
wake-listener (Firestore, service-account hosts only) to v2-A (RTDB,
all Firebase host modes).

## What changes

| Layer | v1 | v2-A |
|---|---|---|
| wake event store | Firestore `users/{uid}/wake_requests/` | RTDB `users/{uid}/wake_requests/` |
| host listener | Go `firestore.Snapshots` (service-account only) | RTDB SSE REST (any Firebase ID token) |
| pair-code mode host | Persistent signaling WS | Wake-listener path |
| `runSignalingFirebase` | Used by pair-code mode | **Removed** |
| presence (`last_seen_at`) | Firestore `users/{uid}/devices/` | RTDB `users/{uid}/devices/` |
| consumed flag | `wake_requests/{rid}.consumed = true` | DELETE the wake_request |
| signaling server WS idle close | none | 60-second default (v2-E) |

End-user impact:
- `peershd -firebase-login` and `peershd -pair-code` now also benefit
  from Cloud Run cost reduction (previously service-account only).
- Mobile must run a build that includes `firebase_database`.

## Prerequisites

- gcloud CLI authenticated against the project (`gcloud auth login`).
- firebase CLI authenticated against the project (`firebase login`).
- Flutter 3.24+ with JDK 17 on PATH for APK rebuild.
- `local/firebase_options.dart.real`, `local/google-services.json.real`,
  `local/app-firebase.json.real` present.

## Cutover order

The migration is a coordinated upgrade — old binaries do **not**
interoperate with new ones during the window between mobile and host
deploys. Plan for a short cutover (minutes, not days).

### 1. Create the Realtime Database instance (one-time)

The Firebase RTDB CLI does not support non-interactive instance
creation; do this from the console.

1. Open <https://console.firebase.google.com/project/PROJECT_ID/database>
2. **Realtime Database → Create Database**
3. **Region**: `asia-southeast1` (Singapore). `asia-northeast1` is not
   supported by RTDB. Update `app/lib/services/rtdb.dart` if you pick
   a different region.
4. Start in **Locked mode**. Real rules are deployed in the next step.

### 2. Deploy RTDB rules

```bash
cd firebase
firebase deploy --only database --project PROJECT_ID
```

The rules in `firebase/database.rules.json` allow `users/{uid}/...`
read/write only for the matching authenticated uid.

### 3. Deploy updated Firestore rules

```bash
firebase deploy --only firestore:rules --project PROJECT_ID
```

This removes the now-unused `wake_requests` rule. The `devices` rule
stays because the signaling server's Register handler still calls
`PutDevice` via Firestore (server-side bypasses rules anyway, but
the rule keeps client-side debugging possible).

### 4. Cloud Run signaling redeploy (optional)

Cloud Run gets the v2-E idle close defense layer. Without redeploy,
old server keeps running — clients still talk to it fine, just no
idle close. Redeploy when convenient:

```bash
PROJECT_ID=PROJECT_ID REGION=asia-northeast1 \
  bash server/deploy/cloud-run/deploy.sh
```

Optional env var to override the default 60 s idle:

```bash
gcloud run services update peersh-signaling --region=REGION \
  --update-env-vars=PEERSH_SIGNALING_IDLE_TIMEOUT=120s
```

### 5. Rebuild and distribute mobile APK

```bash
scripts/build-mobile-core.cmd android   # rebuild peersh.aar
scripts/build-apk-distrib.cmd           # rebuild release APK
```

Distribute the new `app/build/app/outputs/flutter-apk/app-release.apk`
and have every mobile user install it. The new app writes wake
requests to RTDB and uses RTDB for presence + device discovery.

### 6. Rebuild and distribute peershd

```bash
scripts/build-peershd-distrib.cmd
```

Distribute `local/peershd.exe`. Hosts running the old binary keep
trying to register against Cloud Run with persistent WS — they still
work but get no wake events from the new mobile (the old listener is
on Firestore which the new mobile no longer writes to).

### 7. Verify

- `peershd -firebase-login` (or `-pair-code 123456`) starts the host.
  Log shows `wake-pump: registered` and idle thereafter — no
  `signaling: registered` until a wake fires.
- `netstat -an | findstr :443` on the host shows no Cloud Run
  WebSocket; only the RTDB SSE TCP connection to
  `*.firebasedatabase.app`.
- Mobile connect: peershd log shows `wake received` → short WS open →
  `Connect request` → `WS closed`.
- Cloud Run metrics: `peersh_ws_active_connections` near 0 between
  sessions (was non-zero with persistent WS).

## Rollback

If a problem surfaces with v2-A, revert by reinstalling the v1
artifacts. The repo state can be reverted commit-by-commit too.

```bash
# Identify the v1 commits
git log --oneline | grep -E "v1|v2-A"

# Revert in reverse dependency order: code first, then rules
git revert <v2-A commit>            # removes RTDB code, restores Firestore listener
firebase deploy --only firestore:rules --project PROJECT_ID
                                     # restores Firestore wake_requests rule
```

Both old (Firestore) and new (RTDB) rule sets can coexist on the
project — leaving RTDB enabled with the v2-A rules is harmless.
Distribute the old peershd / APK to all users.

## Post-migration cleanup

After a few weeks of stability, optional housekeeping:

- Remove the unused Firestore `users/{uid}/devices` writes from the
  signaling server (it currently runs both the Firestore PutDevice and
  the RTDB host-side last_seen_at, with the RTDB version being
  authoritative for the mobile picker). Leaving both is fine for v2-A.
- Decommission the dead `onSessionCreated` Cloud Function if a future
  v2-D plan does not revive it.

## Things that did NOT need a TTL automation task

The original v2-G task ("Firestore TTL policy IaC") was scoped around
`wake_requests.expires_at`. After v2-A removed wake_requests from
Firestore, no remaining Firestore collection needs a TTL policy:

- `users/{uid}/devices/` — long-lived presence
- `users/{uid}/sessions/` — never written (dead code)
- `users/{uid}/pairings/` — long-lived per-pair record
- `pairing_codes/{code}` — managed by Cloud Function logic (5 min)
- `ops/budget-state` — admin-managed

RTDB has no native TTL primitive; wake_request documents are removed
by host DELETE. Crashed hosts leak entries (small, ignored by the
listener filter); a future cleanup-cron Cloud Function is possible
but unnecessary at current scale.
