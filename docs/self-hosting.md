# Self-Hosting peersh

This guide walks you through running your own peersh signaling server. Self-hosting requires nothing beyond a small Linux VPS (or any host that can run Docker), a `peersh-signaling` binary, and out-of-band delivery of a pre-shared key (PSK) to the user accounts you create.

The signaling server is **only used for connection setup** — pairing devices and exchanging endpoint candidates. Actual PowerShell sessions flow peer-to-peer over QUIC and never touch the server. See `architecture.md` for the full data-flow.

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

Save the `secret` line — it is the only time the raw PSK is displayed. Distribute it out of band (an encrypted message, a printed slip, a password manager) to the user.

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

Two options:

### Option A — terminate TLS at a reverse proxy

Recommended. Run `peersh-signaling` on plain HTTP behind Caddy / Traefik / nginx, which handles certificate provisioning (Let's Encrypt) and renewal. Set `tls.cert_file` and `tls.key_file` to empty strings in the config and pass `-insecure-http` to the binary; only the proxy needs internet exposure.

Caddy snippet:

```caddyfile
signaling.example.com {
  reverse_proxy /ws localhost:8443
  reverse_proxy /healthz localhost:8443
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
- `db_path` — SQLite file path. The Docker image defaults to `/data/peersh-signaling.db` so the named volume retains state. Override with `PEERSH_SIGNALING_DB`.
- `tls.cert_file` / `tls.key_file` — path to PEM cert and key. Both empty → plain HTTP.
- `clock.skew` — how far `signed_at_unix` on a Register may be from server time before rejection (default `60s`).
- `clock.nonce_window` — how long `(user_id, nonce)` pairs are remembered for replay protection (default `5m`).
- `rate_limit.*` — per-IP, per-user, per-device token-bucket settings.

Environment-variable overrides (`PEERSH_SIGNALING_*`):

| Variable | Overrides |
|---|---|
| `PEERSH_SIGNALING_LISTEN` | `listen_addr` |
| `PEERSH_SIGNALING_DB` | `db_path` |
| `PEERSH_SIGNALING_TLS_CERT` | `tls.cert_file` |
| `PEERSH_SIGNALING_TLS_KEY` | `tls.key_file` |
| `PEERSH_SIGNALING_LOG_LEVEL` | `log_level` |
| `PEERSH_SIGNALING_CLOCK_SKEW` | `clock.skew` |
| `PEERSH_SIGNALING_NONCE_WINDOW` | `clock.nonce_window` |
| `PEERSH_SIGNALING_IP_PER_MINUTE` | `rate_limit.ip_per_minute` |

## PSK lifecycle commands

```sh
peersh-signaling psk add    --user alice --label "alice-laptop"
peersh-signaling psk list
peersh-signaling psk revoke --user alice
```

`add` refuses to overwrite an existing record for the same user_id; revoke first if you need to rotate. `revoke` leaves the row in place (so existing in-flight signed requests don't suddenly start failing on a "user not found" error during the skew window) — once revoked, the auth provider returns `psk: psk revoked`.

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

## What's next

- Phase 3 will add real NAT hole-punching so two clients on different home networks can reach each other without manual port forwarding or a VPN.
- Phase 4 introduces the mobile app and explicit pairing UX.
- Phase 5 adds an officially hosted Firebase-backed option as an alternative to self-hosting.
