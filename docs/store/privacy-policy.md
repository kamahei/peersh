# peersh privacy policy

_Last updated: 2026-05-01_

This document describes what data peersh collects, where it travels, and
how it is stored. It applies to:

- The peersh Android app published on Google Play under the package
  `dev.peersh.app`.
- The `peersh-cli` and `peershd` companion binaries you run on your
  Windows host.
- The `peersh-signaling` server (whether you run your own or use a
  hosted instance).

## Plain-language summary

peersh is a tool that runs PowerShell on your home Windows PC from
your phone. The shell traffic flows **directly between your phone and
your PC** over a peer-to-peer QUIC + TLS 1.3 connection. A small
signaling server is only used for connection setup and never sees the
commands or their output.

peersh has **no cloud account, no analytics, no ads, no third-party
SDKs that report usage off-device**.

## What we collect, by surface

### The Android app (`dev.peersh.app`)

| Data | Stored where | Sent off-device? |
|---|---|---|
| Server entries you save (URL, user id, hex PSK) | Android Keystore via `flutter_secure_storage` | No. |
| Persisted PTY reattach handles | Android Keystore | No. |
| Settings (line wrap, font size) | Android Keystore | No. |
| Diagnostic logs | Android logcat (transient, not collected) | No. |

The app does not contain Google Analytics, Firebase Analytics,
Crashlytics, AdMob, or any other third-party telemetry SDK.

### The connection (your phone ↔ your PC)

While a session is open, your phone and your Windows host exchange:

- The PowerShell **commands you type** and the **output the host
  emits**.
- Terminal resize events.
- The session's current working directory (so the file browser can
  scope to it).
- File contents you explicitly request via the file browser.

These bytes are end-to-end encrypted under TLS 1.3 inside QUIC. They
**do not pass through the signaling server** at any point.

### The signaling server

The signaling server only sees connection-setup metadata:

- The pre-shared key user id (`alice`, etc.) that authenticated.
- The IP addresses of the phone and the host (for NAT-traversal
  hole punching).
- Timestamps of connection attempts.
- Auth failures (rate-limited; the server logs the source IP and the
  failure reason).

It **does not see** PowerShell commands, output, file contents, or
the QUIC payload bytes. Those flow peer-to-peer.

If you self-host, you control the retention policy on your own logs.
Operators of public-facing instances should set their own retention
policy (the project recommends ≤ 30 days for abuse-investigation).

### The Windows host (`peershd`)

`peershd` is the binary you run on your home PC. It:

- Spawns a PowerShell child process under a pseudo-console (ConPTY).
- Forwards keystrokes and PTY output to / from the connected client.
- Tracks the shell's current working directory by parsing OSC 9;9
  prompt sequences.
- Optionally reads files you explicitly request via the file browser.

`peershd` does not phone home. It opens a WebSocket to whichever
signaling URL you configured and a QUIC connection direct to the
phone; no other outbound traffic.

## Permissions the Android app requests

| Permission | Why |
|---|---|
| `INTERNET` | open the QUIC + signaling connections. |

The app does not request access to your contacts, photos, location,
microphone, camera, files, or any other personal data category.

## Data retention

- **On the phone**: server entries + reattach handles are kept until
  you delete the server entry or uninstall the app.
- **On your PC**: command history is whatever PowerShell itself
  remembers (`(Get-PSReadlineOption).HistorySavePath`). peersh adds
  no separate log.
- **On the signaling server**: see the table in "The signaling server"
  above.

## Children

peersh is a developer / sysadmin tool. It is not directed at children
under 13 and we do not knowingly collect data from anyone in that age
range.

## Open source

peersh is published under the Apache License 2.0. The full source code
is at <https://github.com/kamahei/peersh>. You can audit anything in
this policy directly against the code.

## Contact

Security disclosures: see `SECURITY.md` in the repository root.

General questions about this policy: file an issue on GitHub.
