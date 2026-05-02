# Firebase mode operations

Firebase mode replaces PSK signing with Firebase Auth on the signaling
WebSocket. Cost-sensitive deployments also benefit from the *wake
listener*: peershd keeps the signaling WS closed between sessions and
opens it for a few seconds in response to a mobile-side wake request
written to the Realtime Database.

## Host modes

The host has three Firebase bootstrap paths. All three converge on the
same wake-listener runtime and the same RTDB-backed wake events.

### `-firebase-login` (Google sign-in via browser)

Run once on a desktop with a browser. Opens an OAuth consent flow,
exchanges the result for a Firebase refresh token, persists it to
`%LOCALAPPDATA%\peersh\firebase-refresh-token.txt`. Subsequent runs
mint Firebase ID tokens from the persisted refresh token.

### `-pair-code 123456`

Headless / no-browser variant. The mobile app's Pair PC screen issues
a 6-digit code via the `mintPairingCode` Cloud Function; peershd
exchanges it for a refresh token via `claimPairingCode`. Same
persisted refresh token output.

### `-firebase-credentials path/to/sa.json`

Operator-managed service account JSON. The service account can mint
custom tokens for any Firebase user; peershd narrows that to a single
uid via `-firebase-email` or `-firebase-uid`. Useful for fully
unattended hosts with no browser and no human-typed pairing.

All three modes produce Firebase ID tokens via the same `TokenSource`
interface; the wake-listener runtime is mode-agnostic.

## Wake sequence

```
mobile               Realtime Database               peershd
  | Firebase sign-in    |                              | RTDB SSE listener (idle):
  |                     |                              |   GET /users/{uid}/wake_requests.json
  |                     |                              |   Accept: text/event-stream
  |                     |                              |   ?auth=<firebase id token>
  | wake_request push -> event: put ----------------->| WS Dial -> Hello -> Register
  |                                                    | Recv Connect, peerauth.Allow
  |                                                    | Punch, SendConnect (host cands)
  |<------ Connect (host candidates) -----------------|
  | Punch, QUIC handshake                              |
  |== QUIC up =========================================| WS close, DELETE wake_request
  | pty / exec ...
```

WS open time on each side is on the order of a few seconds. The RTDB
SSE stream goes to `*.firebasedatabase.app` (not Cloud Run), so the
persistent connection contributes nothing to Cloud Run billing.

## Realtime Database setup (one-time, per Firebase project)

The Go RTDB client targets a single database instance per project.
Create it manually before the first deploy:

1. Firebase Console -> Build -> Realtime Database -> Create Database.
2. Region: pick `asia-southeast1` (Singapore) for jp users; `europe-west1`
   or `us-central1` are the other Firebase RTDB regions.
   `asia-northeast1` is **not** a valid RTDB region.
3. Start in "Locked mode". The `firebase deploy --only database` step
   below uploads the real rules.
4. Deploy rules + verify:

   ```
   firebase deploy --only database --project <project-id>
   ```

If the project has multiple Firebase apps (e.g. Android + iOS), the
single default database is shared by all of them.

## Presence model

`/users/{uid}/devices/{deviceId}/last_seen_at` (RTDB, epoch ms) is
refreshed by peershd every 5 minutes via a single PUT. Mobile clients
read this once before issuing a wake request and warn (but still
attempt) when the timestamp is older than 11 minutes (5 min heartbeat
x 2 + 60 s buffer).

## Wake request payload

```
/users/{uid}/wake_requests/{auto-id}
{
  target_device_id: <16-char base32 deviceId>,
  created_at: ServerValue.timestamp,
  mobile_device_id?: <optional, for log correlation>
}
```

The host deletes the entry immediately after handling the wake. RTDB
SSE then propagates the deletion as `event: put` with `data: null`,
which the listener ignores. Crashed hosts leave dead entries; they
are harmless because the listener filters by `target_device_id` and
the entries are tiny.

## Known limitations

- **PC sleep / hibernation.** peershd must be running to receive wake
  requests. Sleeping or hibernating the PC drops the SSE stream;
  wake-up from a fully suspended state is out of scope. Configure the
  Windows power plan so the PC stays awake.
- **Single RTDB region per project.** The mobile app constructs the
  database URL from the Firebase project id + a hard-coded region
  (`asia-southeast1` today). Operators in other regions need to edit
  `app/lib/services/rtdb.dart` and rebuild the APK.
- **No automatic firebase_options.dart `databaseURL` field.** Until
  `flutterfire configure` is re-run after creating the RTDB instance,
  the mobile app sources the URL from the helper above instead of
  FirebaseOptions.

## Observability

### Server-side (Cloud Run `/metrics`)

Token-gated; set `PEERSH_SIGNALING_METRICS_TOKEN` and scrape with
`Authorization: Bearer <token>`. Metrics relevant to v2-A:

| Metric | Type | What it tells you |
|---|---|---|
| `peersh_ws_active_connections` | gauge | Currently-registered WebSockets. Steady state should be near 0 in Firebase mode. |
| `peersh_ws_session_duration_seconds` | histogram | Per-connection WS lifetime. v2-A target: P95 < 20 s. |
| `peersh_ws_register_to_first_connect_seconds` | histogram | Server-side proxy for "how cold was the host" — high P95 means mobile is racing ahead of host wake. |
| `peersh_ws_idle_closed_total` | counter | Connections the server tore down for inactivity (defense layer; should be near 0). |

### Host-side (peershd `/metrics`, default `127.0.0.1:9101`)

Bound to loopback by default — no token required. Set
`-metrics-addr 0.0.0.0:9101` for remote scraping; that path
mandates `-metrics-token` (or `PEERSH_METRICS_TOKEN` env).

| Metric | Type | What it tells you |
|---|---|---|
| `peersh_wake_event_received_total` | counter | Wake events the SSE listener surfaced. |
| `peersh_wake_event_latency_seconds` | histogram | Mobile-write → host-receive elapsed. Target P95 < 1 s. |
| `peersh_signaling_ws_open_seconds` | histogram | Per-wake host-side WS lifetime. Target P95 < 20 s (capped by `wakeShortTTL` + drain). |
| `peersh_heartbeat_total{result}` | counter vec | RTDB `last_seen_at` write outcomes. `failure` rate > 0 means RTDB connectivity issue. |
| `peersh_rtdb_listener_reconnect_total` | counter | SSE stream re-establishments (token refresh + transient errors). |
| `peersh_rtdb_listener_active` | gauge | 0 / 1 — is the SSE stream currently connected. |

### Example PromQL

```promql
# P50 / P95 wake-event delivery latency over 5 minutes
histogram_quantile(0.50, rate(peersh_wake_event_latency_seconds_bucket[5m]))
histogram_quantile(0.95, rate(peersh_wake_event_latency_seconds_bucket[5m]))

# Heartbeat failure ratio
rate(peersh_heartbeat_total{result="failure"}[5m])
  / ignoring(result) sum without (result) (rate(peersh_heartbeat_total[5m]))

# Server idle-close rate (frozen-client indicator)
rate(peersh_ws_idle_closed_total[5m])
```

## Operational metrics worth watching

- Cloud Run `request_count` and `request_latencies`: should drop
  sharply once Firebase-mode hosts are deployed.
- Firestore writes: the dominant remaining writer is the signaling
  server's Register handler (one PutDevice per wake — same as v1).
- RTDB writes: ~12/hour/device for heartbeats; one PUT + one DELETE
  per wake from the host; one POST per session from the mobile.
- RTDB reads: one snapshot read per session from the mobile (presence
  freshness check). Wake delivery counts are tracked under "open
  connections" rather than read units.
