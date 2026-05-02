# Security policy

## Reporting a vulnerability

If you find a security issue in peersh, please **do not open a public
GitHub issue**. Send a private report instead:

- Email: `security@peersh.dev` (PGP key on request).
- GitHub Security Advisory: open a private advisory in the
  [peersh repository](https://github.com/peersh/peersh) using
  *Security → Report a vulnerability*.

We aim to acknowledge new reports within **3 business days** and to ship
a fix or workaround within **30 days** for confirmed vulnerabilities. We
will keep you informed during the disclosure window and credit you in
the release notes unless you prefer to remain anonymous.

## What's in scope

- The `peersh-signaling` server (`server/`).
- The `peershd` Windows host (`windows/`).
- The `peersh-cli` developer client (`cli/`).
- The `mobile-core/` Go module and the Flutter app under `app/`.
- The Cloud Function and Firestore rules under `firebase/`.
- The QUIC + signaling protocols defined in `proto/`.

Authentication, authorization, NAT traversal, and TLS configuration
issues all qualify. Cost-discipline regressions on the Firestore path
also qualify if they would let an attacker run up an operator's bill.

## What's out of scope

- **Endpoint compromise.** Malware on a paired phone or PC is outside
  peersh's threat model; the user's device is the trust anchor.
- **Brute-force PSK guessing on a self-host operator's deployment.** The
  rate limiter discourages this but is not a substitute for choosing a
  high-entropy PSK. We document this in `docs/deploy/self-hosting.md`.
- **Issues in third-party dependencies** unless peersh's usage of them
  is itself the bug. For example, a quic-go fingerprinting issue is
  upstream; peersh leaving its dev cert in production is ours.
- **Findings against an unsupported branch** or a fork.

## Trust model snapshot

- Signaling servers cannot read PowerShell command content. All command
  bytes flow peer-to-peer over QUIC with TLS 1.3, and the QUIC handshake
  is mutually authenticated (mTLS): both peers present a self-signed
  cert whose private key is the device's long-lived ed25519 keypair, and
  the client side pins the host's expected `device_id` so a server
  presenting a different key fails the handshake. See
  `core/transport/peertls/`.
- Device identity is derived from a public key
  (`device_id = base32(sha256(publicKey)[:10])`). Register frames are
  rejected if the advertised device ID does not match the advertised
  public key. The same ed25519 key drives both signaling Register and
  the QUIC mTLS cert, so the host sees one consistent identity on both
  channels.
- The `peersh-signaling` operator can see who is connected to whom and
  when (signaling metadata) but not the commands or their output.
- PSK secrets are stored as raw bytes in the SQLite store. Operators
  must keep that file on disk-encrypted media. See
  `docs/deploy/self-hosting.md`.

## Dependency audit policy

The Cloud Functions package (`firebase/functions/`) is gated by
[`audit-ci`](https://github.com/IBM/audit-ci), not by raw `npm audit`.
Use `npm run audit` from that directory; it enforces moderate-level
production advisories with one expiring allowlist entry documented in
`firebase/functions/audit-ci.jsonc`. The current allowlist covers an
unreachable-in-our-code-path advisory in a transitive `firebase-admin`
dependency that has no fix-forward path; the entry is scheduled to be
re-evaluated on 2026-08-01.

## Coordinated disclosure

We follow a coordinated-disclosure model. When a fix is ready:

1. We tag a release with the patched binaries.
2. We publish the security advisory describing the issue, the affected
   versions, and the upgrade path.
3. We credit the reporter unless asked to omit them.

Responsible reporters who follow this process will not be subject to
legal action even if their testing technically violated other terms or
laws. We are an open-source project; act in good faith and we will
reciprocate.
