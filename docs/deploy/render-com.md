# Deploying `peersh-signaling` to Render.com

Render is the **simplest first-time hosting option**: connect a GitHub repo, hit Apply on the Blueprint, and you have a public HTTPS endpoint within 10 minutes. Trade-off: the Starter plan ($7/mo) is required for the persistent disk that keeps PSKs across restarts. Free works for smoke tests but PSKs reset.

## Prerequisites

- Render.com account (free signup).
- The peersh repository pushed to GitHub (private repos work).

## Steps

1. **Push the repo** to GitHub if you haven't already:

   ```sh
   gh repo create <your-account>/peersh --private --source=. --push
   ```

2. **In Render**: New + → Blueprint → Connect GitHub → pick the repo. Render reads `server/deploy/render.yaml` and creates a Web Service plus a 1 GB persistent disk. Wait for the build to finish (5–10 min on first run).

3. **Copy the assigned hostname** (e.g. `peersh-signaling-xxxx.onrender.com`) and set:

   - `PEERSH_SIGNALING_DISCOVERY_WS_URL` = `wss://peersh-signaling-xxxx.onrender.com/ws`
   - `PEERSH_SIGNALING_METRICS_TOKEN` = (paste a `openssl rand -hex 32` value) — gates `/metrics` behind a bearer token. Leave blank to disable `/metrics` entirely (fail-closed).

   Save → Render redeploys.

4. **Open the Render Shell** for the service and create a PSK:

   ```sh
   peersh-signaling psk add --user alice --label alice-laptop
   ```

   The shell prints the secret once. Copy it; it cannot be re-displayed.

5. **On your Windows host**:

   ```sh
   echo <secret-hex> > alice.psk
   peershd -signaling wss://peersh-signaling-xxxx.onrender.com/ws \
           -user alice -psk-file alice.psk
   ```

   `peershd` logs its `device_id` at startup. Copy it.

6. **From the mobile app or `peersh-cli`** connect with the same `-user` + `-psk-file` plus `-target <peershd-device-id>`.

## Notes

- Render's `*.onrender.com` hostname provides automatic HTTPS. The `-insecure-http` flag the container passes is fine — TLS is terminated at Render's edge.
- **Starter plan** ($7/mo) is required for the persistent disk that keeps PSKs across restarts. On Free, PSKs reset every redeploy and the service spins down after 15 minutes idle.
- Render injects `$PORT`; the binary honours it via env-var fallback so `PEERSH_SIGNALING_LISTEN` does not need to be set on Render.

## Free-tier mode (PSKs ephemeral)

If you want to skip the $7/mo and accept that PSKs reset, edit `server/deploy/render.yaml` before applying:

```yaml
plan: free
# remove the disk: block
```

Use the **bootstrap PSK env var** to make the reset transparent:

```sh
PEERSH_SIGNALING_BOOTSTRAP_PSK=alice:<hex>:alice-laptop
```

This re-creates the PSK on every cold start. See [`cloud-run.md`](cloud-run.md) for the same pattern (Cloud Run uses it by default).

## Cost reference

| Plan | Cost | Always on | Persistent disk |
|---|---|---|---|
| Free | $0 | ❌ (sleeps after 15 min idle) | ❌ |
| Starter | $7/mo | ✅ | optional ($0.25/GB/mo) |

For Starter + 1 GB disk: **$7.25/mo total**.

## Files in `server/deploy/`

| File | Purpose |
|---|---|
| `render.yaml` | Render Blueprint that defines the Web Service + persistent disk. |
| `Dockerfile` | Docker image used by Render (and Cloud Run, docker-compose, etc.). |
| `signaling.example.toml` | Annotated reference config; not required for Render deploys. |
