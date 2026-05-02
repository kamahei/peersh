# AGENTS

Durable instructions for AI agents working in the peersh repository. Keep this file short enough to fit in working memory.

## Project in one sentence

peersh is an Apache 2.0 OSS tool for executing PowerShell on a home Windows PC from a mobile device, over a peer-to-peer QUIC connection authenticated with mTLS, coordinated by a signaling server that never sees command content and never relays data.

## Read order

When you start any task in this repo, read in this order:

1. `README.md` — what the project is and how an end user runs it.
2. `docs/design/project-overview.md` — design intent.
3. `docs/design/architecture.md` — component layout + data flow.
4. `docs/design/data-model.md` — wire format + storage.
5. `docs/design/glossary.md` — vocabulary.
6. `CONTRIBUTING.md` — build / test / commit conventions.

If you're shipping changes that touch builds or deploys, also read:

- `docs/build.md`
- `docs/deploy/`
- `docs/backup.md`

## Default working mode

- **Stay in plan mode for non-trivial tasks.** Produce a plan covering scope, design decisions, open questions, and validation **before** writing code. Wait for the plan to be reviewed.
- **Ask design questions liberally.** "I see two reasonable approaches here, which do you prefer?" is the right shape. Decisions in the docs are **current intent, not frozen specs** — they can evolve.
- **Keep changes scoped.** Don't preemptively refactor. Don't add features the task doesn't need.
- **Update the docs when assumptions change.** If your work reveals that a doc is wrong or stale, propose an edit as part of the change.
- **Default to small, reviewable steps.** "Smallest thing that works end-to-end" beats half-finished feature completeness.

## Non-negotiable rules

These hold across all changes. Violating them is a regression even when the current task does not exercise them.

- **No relay/TURN, ever.** Signaling is connection-setup-only. NAT-traversal failure surfaces an actionable error; nothing relays data.
- **Signaling never carries command data.** Command bytes never flow through the signaling server.
- **mTLS-derived identity.** `device_id = base32(sha256(publicKey)[:10])` (16 ASCII chars). The same key is identity and TLS credential.
- **No Firebase types in `core/` outside `core/auth/firebase/` and `core/store/firestore/`.** Importing the Firebase SDK from anywhere else is a layering violation.
- **No per-connection state in package globals.** State that varies per connection lives in structs you can construct and tear down.
- **Pluggable interfaces don't change shape.** `auth.Provider` and `store.Store` are public surface. Adding a new provider/store means implementing the interface, not editing `core/`.
- **External `net.PacketConn` for transport.** `core/transport/` accepts an externally-supplied `net.PacketConn` so hole-punching can reuse the socket.
- **Protocol stability.** Breaking changes require bumping `protocol_version`. Optional features go through `capabilities` strings on the Hello messages.
- **Cost discipline (Firebase mode).** ≤ ~5 Firestore reads + ~2 writes per connection lifecycle. No real-time listeners for signaling. Client-side caching for device info and public keys.
- **Apache 2.0 only.** All first-party code is Apache 2.0. Don't pull in dependencies whose license is incompatible.

## Recurring themes

Worth re-reading at the start of any sizeable change:

- **Open-source readiness.** All code is written assuming public review.
- **Pluggability.** Auth and store interfaces stay in their final shape even when only some implementations ship.
- **Privacy / threat model.** The signaling server operator cannot see command content. mTLS-derived identity (via public key fingerprints stored in the trusted directory — Firestore in Firebase mode, SQLite in PSK mode) prevents impersonation.
- **Cost discipline.** ≤ ~5 reads + ~2 writes per connection lifecycle. Client-side caching. No realtime listeners for signaling.
- **Protocol stability.** `protocol_version` for breaking changes; `capabilities` for additive changes.

## Output rules

- **Code and docs are written in English.**
- **Chat replies follow the user's language.**
- **No emojis** unless explicitly asked.
- **No comments that explain what the code does.** Identifiers carry that. Comments justify *why* a non-obvious choice was made (a hidden constraint, a workaround, an invariant).
- **README.md is human-facing**; this file is agent-facing. Don't blur the line.

## When in doubt

Ask the user. The docs encode current intent; the user is the source of truth for intent. "Two reasonable approaches: A or B. A is better for reason R, B for reason S. Which do you want?" beats silently picking.
