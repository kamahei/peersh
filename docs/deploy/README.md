# Deploy guides

Pick the target that fits your situation and budget. The same Docker image (`server/deploy/Dockerfile`) is used by every option below.

## Comparison

| Target | Doc | Fixed cost | Setup time | Persistent PSK | Notes |
|---|---|---|---|---|---|
| **GCP Cloud Run** | [`cloud-run.md`](cloud-run.md) | $0 (free tier covers personal use) | 15 min | ⚠️ via `bootstrap_psk` env var | Pay-per-use, scales to zero, ephemeral filesystem |
| **Render.com Blueprint** | [`render-com.md`](render-com.md) | $7.25/mo (Starter + 1 GB disk) | 5 min | ✅ on Starter | One-click GitHub deploy, persistent disk |
| **Docker / VPS / bare metal** | [`self-hosting.md`](self-hosting.md) | varies | 10–30 min | ✅ | Full control; bring-your-own TLS termination |
| **Firebase mode (Cloud Run + Firestore + RTDB + Functions)** | [`firebase.md`](firebase.md) | $0 within free tier (Spark up to 100 hosts; Blaze beyond) | 30–60 min | ✅ Firestore + RTDB | Google sign-in, Realtime-Database-based wake-event delivery, multi-PC picker |

## Recommended path for a new operator

1. **Start with [`cloud-run.md`](cloud-run.md)** for personal hosting at $0 fixed cost. Learn the moving parts (PSK, signaling URL, peershd, mobile app) without committing to a subscription.
2. Once the workflow feels right, decide whether you want:
   - **Always-on persistent disk** → switch to Render Starter (`render-com.md`) or move the SQLite file to Cloud Storage (Cloud Run + GCS FUSE).
   - **Google sign-in + multi-PC** → flip to Firebase mode (`firebase.md`), where Firestore handles persistence, Google sign-in replaces PSK, FCM wakes Windows hosts, and the mobile app surfaces a per-uid device picker.

## Operations (target-agnostic)

[`self-hosting.md`](self-hosting.md) holds everything that is **not** specific to a hosting target:

- TLS termination strategies (reverse proxy vs. in-binary)
- Configuration reference (TOML + env vars)
- PSK lifecycle (`psk add` / `list` / `revoke`)
- Bootstrap PSK env var (for ephemeral filesystems)
- Prometheus `/metrics`
- Windows Service + Logon Task install on the host (`peershd`)
- Security notes
- Verification + Troubleshooting

Read it after picking a target above.

## Running a host

The guides above deploy the **signaling** server. The **host** (`peershd`) runs on your own computer and connects out to that signaling server:

- **Windows** — the "Windows host service / logon task" section of [`self-hosting.md`](self-hosting.md).
- **macOS** — [`macos-host.md`](macos-host.md): build, login-shell behavior, and the per-user LaunchAgent that starts the host at login.
