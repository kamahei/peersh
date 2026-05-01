# Google Cloud Run deploy for `peersh-signaling`

Cloud Run is the recommended **no-fixed-fee** target. The free tier (2M requests / month, 360 000 GiB-seconds memory, 180 000 vCPU-seconds, 1 GB egress to North America) covers personal use easily; cold-start spin-down means you pay $0 when nobody is connecting.

## Files

| File | Purpose |
|---|---|
| `cloudbuild.yaml` | Cloud Build config that runs `docker build -f server/deploy/Dockerfile`. |
| `deploy.sh` | One-shot script: enables APIs, creates Artifact Registry repo, builds, deploys. |

## Prerequisites

1. `gcloud` CLI installed and logged in (`gcloud auth login`).
2. A GCP project with billing enabled. (Free tier still requires a billing account on file.)
3. APIs enabled: Cloud Run, Cloud Build, Artifact Registry. The script handles this.

## One-line deploy

```sh
PROJECT_ID=<your-project-id> REGION=asia-northeast1 \
  server/deploy/cloud-run/deploy.sh
```

The script prints the assigned `https://...run.app` URL plus the next-step commands.

## Trade-offs vs Render Starter

| Aspect | Cloud Run | Render Starter |
|---|---|---|
| Fixed cost | $0 (pay-per-use) | $7/mo |
| Free tier covers personal use | ✅ | ❌ |
| Cold start | ~2–10 s on first request after idle | always-on |
| Persistent disk for SQLite | ❌ (filesystem is `tmpfs`) | ✅ ($0.25/GB/mo) |
| HTTPS | automatic at `*.run.app` | automatic at `*.onrender.com` |
| Shell into the running container | ❌ (no equivalent) | ✅ (Render Shell tab) |

## How the PSK reset problem is solved

Cloud Run's filesystem is `tmpfs`. Each cold start gets a fresh empty SQLite DB → PSKs would normally be lost.

`peersh-signaling` mitigates this with **bootstrap PSKs** (Phase 7 polish): set
`PEERSH_SIGNALING_BOOTSTRAP_PSK="alice:<hex_secret>:alice-laptop"` and the
container re-creates that PSK record on every startup. As long as the env
var is configured, your client's PSK file keeps working across cold
starts and redeploys.

Multiple users:

```
PEERSH_SIGNALING_BOOTSTRAP_PSK="alice:abc123...:alice-laptop,bob:def456...:bob-phone"
```

## Migrating to the Phase 5 (Firestore + Firebase Auth) path

Once you stand up a Firebase project, the same Cloud Run service can be
flipped to Phase 5 mode by updating env vars:

```
PEERSH_SIGNALING_AUTH_PROVIDER=firebase
PEERSH_SIGNALING_STORE_BACKEND=firestore
PEERSH_SIGNALING_FIREBASE_PROJECT_ID=<your-firebase-project-id>
```

(Plus the IAM permissions on the runtime service account; see
`docs/firebase-setup.md`.) The `bootstrap_psk` env var becomes a no-op
in Firebase mode.
