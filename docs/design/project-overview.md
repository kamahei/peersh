# Project Overview

## Problem

People who run powerful Windows machines at home — gaming rigs, workstations, build boxes — increasingly want to reach them from a phone while away. The realistic options today force a tradeoff: expose the machine via a VPN that needs server infrastructure and ongoing maintenance, route everything through a relay service that sees plaintext or charges per byte, or give up and wait until you're home.

`peersh` exists to remove that tradeoff for the narrow but useful case of **executing PowerShell commands** on a home Windows PC from a mobile device. The data path is peer-to-peer; the only thing on the network besides the two endpoints is a small signaling server that helps them find each other, and that server cannot see what is being run.

## Users

- **Home power users** who want a phone-based way to poke at their Windows machine (start a build, check a process, restart a service) without a VPN.
- **Self-hosters** who already run a small VPS or home server and prefer to operate the signaling component themselves rather than depend on a SaaS.
- **Privacy-conscious users** who want end-to-end encryption to be a structural property of the system rather than a configuration toggle.
- **OSS contributors** who care about a clean, pluggable Go codebase and a Flutter mobile app worth contributing to.

## Goals

- Provide a working mobile → home Windows PowerShell execution path over the public internet without operating any data-plane relay infrastructure.
- Ship two equally first-class deployment modes: a self-hostable single-binary / Docker signaling server for everyone, and an optional Firebase-backed mode (Google sign-in, FCM wake-up, multi-PC picker) for operators who want them.
- Make the wire protocol stable, documented, and versioned from day one.
- Keep the Firebase mode's running cost near zero at low-thousands-of-users scale by carefully shaping signaling-server access patterns.
- Preserve a coherent shell experience: a reconnecting client should be able to attach back to the same `pwsh` session it left (cwd, variables intact).
- Be releasable as Apache 2.0 open source, with package boundaries and design choices that hold up under public review.

## Non-goals

- **No relay/TURN.** When NAT traversal cannot succeed (e.g. CGNAT on both ends), peersh fails with an actionable error. It does not fall back to relaying traffic.
- **No general remote desktop, file transfer, or arbitrary port forwarding.** The product is scoped to PowerShell command execution.
- **No OIDC, OAuth2, or SSO providers** in the initial roadmap. The auth interface is pluggable so they can be added later, but they are not built now.
- **No cross-platform host support.** The host side is Windows only. Linux/macOS hosts are not in scope.
- **No proprietary or non-OSS dependencies** in the core or self-host path. Firebase is allowed only behind the optional Firebase auth/store providers.

## Success signals

- A user with a Windows PC and an Android phone can pair them once and execute PowerShell commands from the phone with no port forwarding or VPN.
- A user with a small VPS can `docker run` a peersh signaling server in under five minutes, point the mobile app at it, and use the system without any Firebase account.
- A signaling server in Firebase mode costs effectively nothing to run for hundreds to low thousands of users.
- The wire protocol survives at least one minor capability addition without a `protocol_version` bump.
- The codebase is organized clearly enough that a new contributor can locate "where would I add a new auth provider" or "where would I add a new store backend" within a few minutes of reading.

## Core design principles

1. **No relay/TURN.** Signaling only. P2P fails gracefully when NAT traversal cannot succeed (CGNAT both sides, etc.).
2. **End-to-end encryption.** QUIC mandates TLS 1.3. The signaling server cannot see command content.
3. **Authentication is pluggable.** Three providers planned: None, PSK, Firebase. Adding more is a matter of implementing the interface.
4. **Storage is pluggable.** Same approach: Memory, SQLite, Firestore as initial backends.
5. **Self-hosting must be simple.** A user with a small VPS should be able to `docker run` a signaling server in under five minutes with no Firebase account.
6. **Cost-conscious.** The official hosted server should fit in the Firebase/GCP free tier for hundreds to low thousands of users. Design Firestore access patterns accordingly (≤ ~5 reads + ~2 writes per connection lifecycle, client-side caching, no realtime listeners for signaling).
7. **Session continuity.** A reconnecting client should be able to attach to its previous PowerShell session (cwd, variables preserved).
8. **Background persistence (best effort).** Mobile clients should keep the connection alive when backgrounded for a reasonable time, within OS constraints.

## Constraints

- **License.** Apache License 2.0. All first-party code and assets land under this license.
- **Stack.** Go for all backend components (Windows host, signaling server, mobile network layer). Flutter (Dart) for mobile UI. The mobile network layer is shared Go compiled via `gomobile bind`.
- **Transport.** QUIC over UDP, mandatory TLS 1.3.
- **Identity.** Device IDs are derived from the device's public key, not assigned by the server. The same key serves both as identity and as the credential for mTLS.
- **Protocol.** Versioned (`protocol_version` plus `capabilities` strings on every Hello).

## Environment

- **Dev machine.** Windows 10/11 with JetBrains Rider or VS Code.
- **Target host.** Windows 10/11. PowerShell 7 (`pwsh`) preferred; falls back to `powershell.exe` if `pwsh` is not present on PATH.
- **Go.** 1.22 or later.
- **Flutter.** Latest stable.

## Where to read next

- `docs/design/product-spec.md` — capabilities, user journeys, NFRs, and explicit out-of-scope items.
- `docs/design/architecture.md` — system shape, components, transport, NAT strategy, auth and storage interfaces, protocol versioning, repository layout.
- `docs/build.md` — how to build everything from source.
- `docs/deploy/` — operator-side deployment walkthroughs.
