# Firebase mode operations

Firebase mode replaces PSK signing with Firebase Auth on the signaling
WebSocket. It also unlocks an optional cost-saving path on the host:
the *wake listener*, which lets `peershd` keep the signaling WS closed
between sessions and only open it for a few seconds in response to a
mobile-side wake request.

## Host modes

There are two Firebase host configurations. The choice is determined by
which credentials the operator gave to `peershd`.

### Service-account mode (wake-listener path)

Set up with `-firebase-credentials path/to/service-account.json`.

`peershd` opens a Firestore `Listen` gRPC stream over
`users/{uid}/wake_requests`. The signaling WebSocket is closed except
during a short wake window (~15 s after each wake event, with a 5 s
drain after each Connect). Cloud Run request-time billing is
proportional to the number of sessions, not to host uptime.

The Go Firestore SDK (`cloud.google.com/go/firestore`) uses GCP IAM
credentials, which is why this path is gated on a service-account JSON.

### Pair-code mode (persistent-WS fallback)

Set up with `-pair-code 123456` (or the persisted refresh token from a
prior pairing).

`peershd` holds the signaling WebSocket open for the whole process
lifetime. Mobile-side wake requests are written but ignored; the host
already has an active registration and receives Connect frames
directly. Cloud Run is billed for the duration of the WS.

This path exists because pair-code hosts have only Firebase ID tokens
(via the Identity Toolkit refresh-token flow), and the Go Firestore
SDK does not accept those tokens.

If you want the wake-listener cost benefit, configure a
service-account JSON. A future v2 may add a Firestore REST listener
that accepts Firebase ID tokens to lift this restriction.

## Wake sequence (service-account mode)

```
mobile               Firestore                       peershd
  | Firebase sign-in    |                              | wake listener (gRPC stream) idle
  | wake_request write -> snapshot push -------------> | WS Dial -> Hello -> Register
  |                                                    | Recv Connect, Punch, SendConnect
  |<------ Connect (host candidates) -----------------|
  | Punch / QUIC handshake                             |
  |== QUIC up =========================================| WS close, wake_request consumed
  | pty / exec ...
```

WS open time on each side is on the order of a few seconds. Cloud Run's
60-minute request timeout is irrelevant in this mode.

## Firestore TTL on wake_requests

Wake-request documents expire 30 s after creation. Configure a TTL
policy in the Firebase console so old documents are garbage-collected
automatically:

1. Open the Firebase console -> Firestore Database -> TTL.
2. Add a policy on collection group `wake_requests`, field
   `expires_at`.
3. Status takes ~24 hours to apply but is no-op for fresh documents in
   the meantime — the host's `consumed` filter ignores stale entries.

## Presence model

`users/{uid}/devices/{deviceId}.last_seen_at` is refreshed by `peershd`
every 5 minutes. Mobile clients use it as a freshness hint before
attempting to connect:

- If the timestamp is within 11 minutes (5 min heartbeat × 2 + 60 s
  buffer), the host is treated as online.
- Older timestamps are reported in the debug log; the dial still
  proceeds (mobile-core's SendConnect retry covers cold-start races).
- Missing documents are treated as "unknown" and the dial proceeds
  optimistically (preserving compatibility with pair-code-mode hosts
  that don't update this field).

## Known limitations

- **PC sleep / hibernation.** `peershd` must be running to receive
  wake requests. Sleeping or hibernating the PC drops the Firestore
  listener; wake-up from a fully suspended state is out of scope for
  v1. Configure the Windows power plan so the PC stays awake.
- **Pair-code mode does not benefit from cost reduction.** It keeps
  the persistent WS for compatibility. Use `-firebase-credentials`
  if Cloud Run billing matters.
- **Hard crash leaves last_seen_at stale.** Without a graceful
  shutdown, `last_seen_at` keeps the value from the last heartbeat.
  Mobile clients fall back to the SendConnect retry loop after a
  failed connection attempt and surface a user-visible error within a
  few seconds.

## Operational metrics worth watching

- Cloud Run `request_count` and `request_latencies`: should drop
  sharply once service-account-mode hosts are deployed.
- Firestore document writes: the dominant new write is the host
  heartbeat (~12/hour/device). Wake_requests are small and TTL-bounded.
- Firebase Cloud Functions invocations: `onSessionCreated` is
  currently dead code (no caller writes `users/{uid}/sessions`); it
  remains for a future v2 path.
