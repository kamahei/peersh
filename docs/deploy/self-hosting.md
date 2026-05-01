# Self-Hosting peersh — Operations Guide

This guide covers the **target-agnostic operational knowledge**: TLS, PSK lifecycle, configuration reference, metrics, security notes, and troubleshooting. For platform-specific deploy walkthroughs see:

- [`cloud-run.md`](cloud-run.md) — GCP Cloud Run (pay-per-use, free tier covers personal use)
- [`render-com.md`](render-com.md) — Render.com Blueprint (zero config, $7/mo Starter for persistence)
- [`firebase.md`](firebase.md) — Phase 5 Firestore + Cloud Functions backend

The signaling server is **only used for connection setup** — pairing devices and exchanging endpoint candidates. Actual PowerShell sessions flow peer-to-peer over QUIC and never touch the server. See `../design/architecture.md` for the full data-flow.

## Prerequisites

- A host with a public TCP port reachable from your devices (port `8443` by default).
- One of:
  - **Docker / Docker Compose** — the recommended path; uses the published image.
  - **Go 1.22+** — if you prefer to build the binary directly.
- A reverse proxy if you want browser-friendly TLS termination (Caddy / Traefik / nginx). Optional in development; recommended in production.

## Quick start (Docker, plain HTTP — development only)

From the repository root:

```sh
cd server/deploy
docker compose up -d --build
```

That brings up `peersh-signaling` listening on `:8443` over plain HTTP. The data volume `signaling-data` holds the SQLite database with users and PSK records.

Generate a PSK for your user:

```sh
docker compose exec signaling peersh-signaling psk add --user alice --label alice-laptop
# PSK created. Save this — it cannot be retrieved later.
#   user:   alice
#   label:  alice-laptop
#   secret: 7a3f5e9c…
```

Save the `secret` line — it is the only time the raw PSK is displayed. Distribute it out of band (an encrypted message, a printed slip, a password manager).

Once the user has the PSK, point peershd and peersh-cli at the signaling server:

```sh
# On the Windows host:
echo 7a3f5e9c… > alice.psk
peershd -signaling ws://signaling.example.com:8443/ws \
        -user alice -psk-file alice.psk \
        -listen :7777

# On any client machine:
echo 7a3f5e9c… > alice.psk
peersh-cli -signaling ws://signaling.example.com:8443/ws \
           -user alice -psk-file alice.psk \
           -target <peershd-device-id>
```

`peershd` logs its `device_id` at startup (`device_id=LG3N25YMXIBFTDQA` style); pass that to `peersh-cli -target`.

## Quick start (binary, no Docker)

```sh
go build -o /usr/local/bin/peersh-signaling ./server/cmd/peersh-signaling
sudo cp server/deploy/signaling.example.toml /etc/peersh/signaling.toml
sudo $(which peersh-signaling) psk add --user alice --label alice-laptop
sudo $(which peersh-signaling) serve -config /etc/peersh/signaling.toml -insecure-http
```

Set up a systemd unit to run it persistently. Use a reverse proxy in front for TLS.

## Production setup with TLS

### Option A — terminate TLS at a reverse proxy (recommended)

Run `peersh-signaling` on plain HTTP behind Caddy / Traefik / nginx, which handles certificate provisioning (Let's Encrypt) and renewal. Set `tls.cert_file` and `tls.key_file` to empty strings in the config and pass `-insecure-http` to the binary; only the proxy needs internet exposure.

Caddy snippet:

```caddyfile
signaling.example.com {
  reverse_proxy /ws localhost:8443
  reverse_proxy /health localhost:8443
  reverse_proxy /metrics localhost:8443
  reverse_proxy /.well-known/peersh.json localhost:8443
}
```

Then clients connect with `wss://signaling.example.com/ws`.

### Option B — terminate TLS in `peersh-signaling`

Provide a cert/key pair directly:

```toml
[tls]
cert_file = "/etc/letsencrypt/live/signaling.example.com/fullchain.pem"
key_file  = "/etc/letsencrypt/live/signaling.example.com/privkey.pem"
```

Run `peersh-signaling serve -config /etc/peersh/signaling.toml` (drop `-insecure-http`). The server listens on `:8443` over HTTPS.

## Configuration reference

The full annotated config lives at `server/deploy/signaling.example.toml`. Key fields:

- `listen_addr` — `host:port` for the HTTP/HTTPS listener. Default `:8443`.
- `auth_provider` — `psk` (default) or `firebase`.
- `store_backend` — `sqlite` (default) or `firestore`.
- `db_path` — SQLite file path. Defaults to `/data/peersh-signaling.db` inside the Docker image.
- `tls.cert_file` / `tls.key_file` — path to PEM cert and key. Both empty → plain HTTP.
- `clock.skew` — how far `signed_at_unix` on a Register may be from server time before rejection (default `60s`).
- `clock.nonce_window` — how long `(user_id, nonce)` pairs are remembered for replay protection (default `5m`).
- `rate_limit.*` — per-IP, per-user, per-device token-bucket settings.
- `discovery.ws_url` — value returned by `/.well-known/peersh.json`.
- `firebase.project_id` — required when `auth_provider = "firebase"` or `store_backend = "firestore"`.

### Environment-variable overrides

Every TOML field has a matching `PEERSH_SIGNALING_*` env var. Env vars override the TOML file:

| Variable | Overrides |
|---|---|
| `PORT` | listen port (when `PEERSH_SIGNALING_LISTEN` is unset; the platform-of-record convention used by Cloud Run / Render / Heroku / Fly) |
| `PEERSH_SIGNALING_LISTEN` | `listen_addr` |
| `PEERSH_SIGNALING_AUTH_PROVIDER` | `auth_provider` |
| `PEERSH_SIGNALING_STORE_BACKEND` | `store_backend` |
| `PEERSH_SIGNALING_DB` | `db_path` |
| `PEERSH_SIGNALING_TLS_CERT` | `tls.cert_file` |
| `PEERSH_SIGNALING_TLS_KEY` | `tls.key_file` |
| `PEERSH_SIGNALING_LOG_LEVEL` | `log_level` |
| `PEERSH_SIGNALING_CLOCK_SKEW` | `clock.skew` |
| `PEERSH_SIGNALING_NONCE_WINDOW` | `clock.nonce_window` |
| `PEERSH_SIGNALING_IP_PER_MINUTE` | `rate_limit.ip_per_minute` |
| `PEERSH_SIGNALING_DISCOVERY_WS_URL` | `discovery.ws_url` |
| `PEERSH_SIGNALING_DISCOVERY_STUN_SERVERS` | `discovery.stun_servers` (comma-separated) |
| `PEERSH_SIGNALING_FIREBASE_PROJECT_ID` | `firebase.project_id` |
| `PEERSH_SIGNALING_FIREBASE_CREDENTIALS` | `firebase.credentials_path` |
| `PEERSH_SIGNALING_BOOTSTRAP_PSK` | seed PSKs at startup (`user:hex[:label],...`) |

## Metrics (Phase 7)

`peersh-signaling` exposes Prometheus metrics at `/metrics`:

- `peersh_ws_upgrade_accepted_total` — successful WebSocket upgrades.
- `peersh_ws_upgrade_rejected_total{reason}` — pre-upgrade rejections.
- `peersh_ws_register_accepted_total` — successful Register frames.
- `peersh_ws_register_rejected_total{reason}` — Register failures.
- `peersh_ws_connect_forwarded_total` — Connect frames the registry routed.
- `peersh_ws_connect_rejected_total{reason}` — Connect routing rejections.
- `peersh_ws_active_connections` — gauge of currently-registered WebSocket connections.

Plus the standard Go runtime / process metrics from `prometheus/client_golang`. Scrape it like any Prometheus target.

## Windows host service / logon task (Phase 7)

`peershd` runs in three modes:

```sh
peershd                                  # interactive (default)
peershd -install                          # register as a Windows Service (SYSTEM context)
peershd -uninstall
peershd -start | -stop | -service-status

peershd -install-logon-task               # register as a Scheduled Task at user logon (current user)
peershd -install-logon-task -logon-task-user "DOMAIN\\Alice"
peershd -uninstall-logon-task
```

Service mode runs `peershd` as the Windows SYSTEM account; the spawned PowerShell session inherits that context. Logon-task mode runs `peershd` as the user who logged in, and the PowerShell session inherits that user — typically the right choice for personal desktops where `peershd` should "follow the user". Pick whichever matches your security model.

## PSK lifecycle commands

```sh
peersh-signaling psk add    --user alice --label "alice-laptop"
peersh-signaling psk list
peersh-signaling psk revoke --user alice
```

`add` refuses to overwrite an existing record for the same user_id; revoke first if you need to rotate. `revoke` leaves the row in place (so existing in-flight signed requests don't suddenly start failing on a "user not found" error during the skew window) — once revoked, the auth provider returns `psk: psk revoked`.

For ephemeral filesystems (Cloud Run / Render Free), use the `PEERSH_SIGNALING_BOOTSTRAP_PSK` env var instead — see `cloud-run.md`.

## Security notes

### PSK secret storage

Phase 2 stores PSK secrets **as raw bytes** in SQLite. HMAC-SHA256 verification needs the secret server-side, so a hash-only scheme cannot work. Trade-off:

- A breach of the SQLite file exposes every PSK directly. **Treat the file as sensitive material.**
- Recommended: host the database on a disk-encrypted volume. On Linux, that's typically an LUKS-encrypted partition or a per-volume `cryptsetup` block device. On a public-cloud VPS, enable provider-side encryption.
- The Docker image runs as a non-root user (UID 65532) and writes only inside `/data`. Mount that volume on encrypted storage.

### TLS termination

Plain HTTP signaling is **only acceptable for local development**. Without TLS:

- Anyone on the network path can read the PSK-signed Register frames (the signature itself is fine, but the user_id is in cleartext).
- Active MITMs can substitute a forged signaling response and lure you into dialing the wrong endpoint. The QUIC mTLS layer is what ultimately authenticates the peer, but without TLS on signaling you lose audit and forward secrecy on the discovery channel.

Use TLS in production. The `peersh-signaling` binary's `-insecure-http` flag exists only to make local tests easy and prints a loud warning when used.

### Rate limiting

The defaults (10 connections/min/IP, 10 registrations/min/user, 30 connects/min/device) are reasonable for personal / small-group use. For larger deployments tune them in the `[rate_limit]` section.

### Pairing model

Phase 2 uses **implicit pairing**: any two devices that authenticate under the same PSK user_id can address each other. A separate explicit pairing token / QR code arrives in Phase 4 with the mobile app. Until then, give each user their own user_id (and PSK) and treat the PSK itself as the device-pair credential.

### Mobile-app discovery (Phase 4a)

The signaling server serves `/.well-known/peersh.json` at its HTTPS root so that the mobile app (Phase 4b) can find the WebSocket endpoint, recommended STUN servers, and supported auth providers from a hostname alone. Operators populate the `[discovery]` section of `signaling.toml` (or set `PEERSH_SIGNALING_DISCOVERY_WS_URL` via env var):

```toml
[discovery]
ws_url = "wss://signaling.example.com/ws"
stun_servers = ["stun.l.google.com:19302"]
```

The endpoint accepts GET and HEAD only.

### NAT traversal (Phase 3)

`peershd` and `peersh-cli` discover their public address via STUN (`stun.l.google.com:19302` by default; override with the `-stun` flag) and include it as a SERVER_REFLEXIVE candidate alongside their LAN addresses. After exchanging candidates through signaling, both sides fire a brief burst of UDP punch packets at the peer's reflexive address to install NAT mappings, then `peersh-cli` QUIC-dials the candidates in preferred order (IPv6-srflx → IPv4-srflx → IPv6-host → IPv4-host). When NAT traversal cannot succeed (typically symmetric CGNAT on both ends), `peersh-cli` exits with "Direct connection not possible from this network." — peersh **never** falls back to a relay.

No additional setup is required: STUN is automatic and uses the same UDP socket QUIC speaks over.

## Verifying a working setup

Once everything is running:

1. From the host: `peershd ... -listen :7777` should log `dev cert ready`, `listening for QUIC`, and `registered with signaling server` within a couple of seconds.
2. From a client machine: `peersh-cli ... -target <device-id>` should log `registered with signaling`, `requesting connect`, `rendezvous complete`, `connected`, and `handshake complete`.
3. At the `peersh>` prompt, run `Get-Process | Select-Object -First 3 | Out-String`. Output should stream back identically to the Phase 1 same-LAN demo.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `signaling: dial ... failed` from the client | The signaling server isn't reachable, or the URL has the wrong scheme. Use `ws://` for plain HTTP, `wss://` for TLS. |
| `signaling: register rejected by server: auth: psk: signature invalid` | Wrong PSK file content or the user ID does not match the PSK's user. Re-generate via `psk add` and re-distribute. |
| `signaling: register rejected by server: auth: psk: signed_at outside acceptable skew` | The client's clock is skewed by more than 60 seconds. Sync NTP. |
| `room: target device is not registered` (returned as a `ServerError`) | The target device id is wrong, or `peershd` isn't currently connected to the signaling server. Check the host's logs. |
| `Dial QUIC: ... no route to host` | The host's advertised candidates aren't reachable from the client's network. Phase 2 assumes cooperative endpoints (same LAN, port forward, or VPN); real NAT traversal arrives in Phase 3. |
