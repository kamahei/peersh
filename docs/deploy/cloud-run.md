# Deploying `peersh-signaling` to Google Cloud Run

Cloud Run is the **pay-per-use** option. The free tier (2 M requests / month, 360 000 GiB-seconds memory, 180 000 vCPU-seconds) covers personal use; cold-start spin-down means you pay $0 when nobody is connecting.

**Trade-off**: Cloud Run's filesystem is `tmpfs`, so the SQLite DB is wiped on every cold start. `peersh-signaling` works around this with **bootstrap PSKs** — set the PSK as an env var, and the container re-creates the record on every startup.

## Prerequisites

- `gcloud` CLI installed and authenticated (`gcloud auth login`).
- A GCP project with **billing enabled**. (Free tier still requires a billing account on file.)
- Source repository checked out locally; the deploy script and `cloudbuild.yaml` live under `server/deploy/cloud-run/`.

## Steps

1. **Install gcloud CLI** if you haven't: <https://cloud.google.com/sdk/docs/install>

2. **Authenticate**:

   ```sh
   gcloud auth login
   ```

3. **Create or pick a GCP project** with billing enabled:

   ```sh
   gcloud projects create peersh-signaling-<your-suffix>      # or skip if you have one
   gcloud config set project peersh-signaling-<your-suffix>

   gcloud beta billing accounts list
   gcloud beta billing projects link peersh-signaling-<your-suffix> \
     --billing-account=<XXXXXX-XXXXXX-XXXXXX>
   ```

4. **Run the deploy script** (from the repo root):

   ```sh
   PROJECT_ID=$(gcloud config get-value project) REGION=asia-northeast1 \
     server/deploy/cloud-run/deploy.sh
   ```

   The script:

   - enables the required APIs (`run`, `cloudbuild`, `artifactregistry`),
   - creates an Artifact Registry repository (`peersh`) if absent,
   - builds the Docker image via Cloud Build (`server/deploy/Dockerfile`) and pushes both `:latest` and `:$BUILD_ID` tags,
   - deploys to Cloud Run as `peersh-signaling`,
   - prints the assigned `https://...run.app` URL plus the next-step commands.

5. **Generate a PSK locally** (any 32-byte hex value works):

   ```sh
   # On Windows PowerShell
   [Convert]::ToHexString((1..32 | %{[byte](Get-Random -Max 256)}))

   # Or via openssl on any Unix
   openssl rand -hex 32
   ```

6. **Wire the PSK + discovery URL into the running service**:

   ```sh
   # Generate a /metrics token so Prometheus telemetry is not exposed publicly.
   METRICS_TOKEN=$(openssl rand -hex 32)

   gcloud run services update peersh-signaling \
     --region=asia-northeast1 \
     --update-env-vars=PEERSH_SIGNALING_DISCOVERY_WS_URL=wss://<host>/ws,\
   PEERSH_SIGNALING_BOOTSTRAP_PSK=alice:<hex>:alice-laptop,\
   PEERSH_SIGNALING_METRICS_TOKEN=$METRICS_TOKEN
   ```

   Replace `<host>` with the `*.run.app` host name from step 4 and `<hex>` with the secret from step 5.

7. **Verify**:

   ```sh
   curl https://<host>/health                                   # → ok  (Cloud Run reserves /healthz at the edge)
   curl https://<host>/.well-known/peersh.json                  # → JSON with the WS URL
   curl https://<host>/metrics                                  # → 404 without the token (fail-closed)
   curl -H "Authorization: Bearer $METRICS_TOKEN" \
        https://<host>/metrics                                  # → Prometheus exposition
   ```

   If `PEERSH_SIGNALING_METRICS_TOKEN` is unset, `/metrics` returns 404 — peersh-signaling fails closed so a misconfigured deploy never silently leaks telemetry to the public internet.

8. **Connect from peershd** on your Windows host:

   ```sh
   echo <hex> > alice.psk
   peershd -signaling wss://<host>/ws -user alice -psk-file alice.psk
   ```

   `peershd` logs its `device_id` at startup. Use that as `-target` from `peersh-cli` or the mobile app's server entry.

## Multiple users

`PEERSH_SIGNALING_BOOTSTRAP_PSK` accepts a comma-separated list:

```
PEERSH_SIGNALING_BOOTSTRAP_PSK="alice:abc...:alice-laptop,bob:def...:bob-phone"
```

## Migrating to Firebase mode (Firestore + Realtime Database + Firebase Auth)

The same Cloud Run service can be flipped to Firebase mode by updating env vars (no rebuild required):

```sh
gcloud run services update peersh-signaling \
  --region=asia-northeast1 \
  --update-env-vars=PEERSH_SIGNALING_AUTH_PROVIDER=firebase,\
PEERSH_SIGNALING_STORE_BACKEND=firestore,\
PEERSH_SIGNALING_FIREBASE_PROJECT_ID=<your-firebase-project-id>
```

The bootstrap-PSK env var becomes a no-op in Firebase mode. The Firebase mode also requires a Realtime Database instance for wake-event delivery (created separately in the Firebase Console; see [`firebase.md`](firebase.md) step 4a). See [`firebase.md`](firebase.md) for the full walkthrough (Cloud Functions, Firestore + RTDB rules, mobile FlutterFire setup, peershd `-firebase-login` / `-pair-code`).

## Optional: idle close defense layer

After v2-E, the signaling server tears down a registered WebSocket that hasn't sent a frame within `IdleTimeout` (default 60 s) with a `ServerError("idle_timeout")` frame. This bounds Cloud Run billing if a frozen client holds the WS open without sending Connect frames. Override per service:

```sh
gcloud run services update peersh-signaling \
  --region=asia-northeast1 \
  --update-env-vars=PEERSH_SIGNALING_IDLE_TIMEOUT=120s
```

Use `-1s` to disable entirely (not recommended for production); `0s` keeps the 60 s default.

## Files in `server/deploy/cloud-run/`

| File | Purpose |
|---|---|
| `cloudbuild.yaml` | Cloud Build config that builds with `server/deploy/Dockerfile` and pushes to Artifact Registry. |
| `deploy.sh` | One-shot script tying API enablement, repo creation, build, and Cloud Run deploy together. |
| `README.md` | Quick reference inside the deploy directory. |

## Trade-offs vs Render Starter

| Aspect | Cloud Run | Render Starter |
|---|---|---|
| Fixed cost | $0 (pay-per-use) | $7/mo |
| Free tier covers personal use | ✅ | ❌ |
| Cold start | ~2–10 s on first request after idle | always-on |
| Persistent disk for SQLite | ❌ (filesystem is `tmpfs`; `bootstrap_psk` works around it) | ✅ ($0.25/GB/mo) |
| HTTPS | automatic at `*.run.app` | automatic at `*.onrender.com` |
| Shell into the running container | ❌ (use `gcloud run services update --update-env-vars` instead) | ✅ (Render Shell tab) |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `gcloud builds submit` fails with API not enabled | Re-run `deploy.sh`; it enables the APIs idempotently. |
| `gcloud run deploy` fails with `Cloud Run service is reading from a private container registry` | Ensure the Cloud Build service account has `roles/artifactregistry.writer` on the repo (default for new projects). |
| `502 Bad Gateway` from the URL | The container failed to start — check `gcloud run services logs read peersh-signaling --region=$REGION`. |
| WebSocket disconnects after ~60 minutes | Cloud Run request timeout. Bump with `--timeout=3600` (already set in `deploy.sh`). |
