# AGENTS

Durable instructions for AI agents working in the peersh repository. The companion file is `docs/ai-implementation-guide.md`, which goes deeper. This file is the short version that should fit in working memory.

## Project in one sentence

peersh is an Apache 2.0 OSS tool for executing PowerShell on a home Windows PC from a mobile device, over a peer-to-peer QUIC connection authenticated with mTLS, coordinated by a signaling server that never sees command content and never relays data.

## Read order

When you start any task in this repo, read in this order:

1. `docs/project-overview.md`
2. The current phase section of `docs/implementation-plan.md`
3. `docs/architecture.md`
4. `docs/data-model.md` (if your task touches entities)
5. `docs/open-questions.md` (for anything not yet decided)
6. `docs/task-breakdown.md` (for the next concrete steps)
7. `docs/acceptance-criteria.md` (to know when you're done)
8. `docs/ai-implementation-guide.md` (operating manual; longer-form version of this file)
9. `docs/glossary.md` (when a term is unfamiliar)

When two docs disagree, follow the precedence in `docs/ai-implementation-guide.md` and **propose a doc update** rather than picking silently.

## Default working mode

- **Default to Plan Mode at the start of every phase.** Even if the user just says "let's do Phase 2", produce a plan covering scope, design decisions, open questions, and validation **before** writing code. Wait for the plan to be reviewed.
- **Ask design questions liberally during planning.** "I see two reasonable approaches here, which do you prefer?" is the right shape. Decisions in the docs are **current intent, not frozen specs** — they can evolve.
- **Keep changes scoped to the current phase.** Don't preemptively refactor for later phases. Don't add features the current phase doesn't need.
- **Update the docs when assumptions change.** If your phase's work reveals that a doc is wrong or stale, propose an edit as part of the phase's plan.
- **Surface risks early.** If you see something in a later phase that the current phase locks us out of, say so now.
- **Default to small, reviewable steps.** "Smallest thing that works end-to-end" beats half-finished feature completeness.

## Non-negotiable rules

These hold across all phases. Violating them is a regression even when the current phase does not exercise them.

- **No relay/TURN, ever.** Signaling is connection-setup-only. NAT-traversal failure surfaces an actionable error; nothing relays data.
- **Signaling never carries command data.** Command bytes never flow through the signaling server.
- **mTLS-derived identity.** `device_id = base32(sha256(publicKey)[:16])`. The same key is identity and TLS credential.
- **No Firebase types in `core/`.** All Firebase/Firestore symbols live under `core/auth/firebase/` or `core/store/firestore/`. Importing the Firebase SDK from anywhere else is a layering violation.
- **No per-connection state in package globals.** State that varies per connection lives in structs you can construct and tear down.
- **Pluggable interfaces in their final shape from Phase 1.** `auth.Provider` and `store.Store` exist from day one, even though only `none` and `memory` ship initially. Adding a new provider/store means implementing the interface, not changing `core/`.
- **External `net.PacketConn` for transport.** `core/transport/` accepts an externally-supplied `net.PacketConn`. This is non-negotiable for Phase 3's hole-punched-socket reuse.
- **Protocol stability.** Once Phase 1 ships, breaking changes require bumping `protocol_version`. Optional features go through `capabilities` strings on the Hello messages.
- **Cost discipline (Firebase mode).** ≤ ~5 Firestore reads + ~2 writes per connection lifecycle. No real-time listeners for signaling. Client-side caching for device info and public keys.
- **Apache 2.0 only.** All first-party code is Apache 2.0. Don't pull in dependencies whose license is incompatible.

## Recurring themes

Mentioned in nearly every phase's plan. Worth re-reading at the start of each:

- **Open-source readiness.** Even Phase 1 code is written assuming public review.
- **Pluggability from day one.** Auth and store interfaces are in their final shape even when only the trivial implementations ship.
- **Privacy / threat model.** The signaling server operator cannot see command content. mTLS-derived identity (via public key fingerprints stored in the trusted directory — Firestore in Firebase mode, SQLite in PSK mode) prevents impersonation.
- **Cost discipline.** ≤ ~5 reads + ~2 writes per connection lifecycle. Client-side caching. No realtime listeners for signaling.
- **Protocol stability.** `protocol_version` for breaking changes; `capabilities` for additive changes. The wire protocol has a public surface from Phase 1 onward.

## How to start a phase

When the user says something like "let's start Phase N":

1. Re-read `docs/project-overview.md`, the relevant phase section in `docs/implementation-plan.md`, and the cross-phase invariants in `docs/acceptance-criteria.md`.
2. Stay in Plan Mode and produce a plan covering scope, design decisions, open questions, deviations from the docs (if any, with justification), and a validation procedure that maps to `docs/acceptance-criteria.md`.
3. List open questions. Pull from `docs/open-questions.md` plus anything new you noticed.
4. Wait for plan review. Begin implementation only after the plan is approved.
5. After the phase, propose updates to the relevant docs if assumptions changed. Move resolved items out of `docs/open-questions.md` into whichever doc owns the answer.

## Output rules

- **Code and docs are written in English.**
- **Chat replies follow the user's language.**
- **No emojis** unless explicitly asked.
- **No comments that explain what the code does.** Identifiers carry that. Comments justify *why* a non-obvious choice was made (a hidden constraint, a workaround, an invariant).
- **README.md is human-facing**; AGENTS.md and `docs/ai-implementation-guide.md` are agent-facing. Don't blur the line.

## When in doubt

Ask the user. The docs encode current intent; the user is the source of truth for intent. "Two reasonable approaches: A or B. A is better for reason R, B for reason S. Which do you want?" beats silently picking.
