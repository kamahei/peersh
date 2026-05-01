# AI Implementation Guide

This document is the operating manual for AI agents working in the peersh repository. Read it whenever you start a task in this repo. The companion file is `AGENTS.md`, which is the durable instruction file the harness loads automatically.

## What peersh is, in one paragraph

peersh is a tool for executing PowerShell on a home Windows PC from a mobile device (iOS / Android) over the public internet. The data path is peer-to-peer over QUIC with mTLS. A signaling server is used only for connection setup and never sees command content. The project will be released as Apache 2.0 open source. Two deployment modes are first-class: an officially hosted signaling server (Firebase-backed) and a self-hostable single-binary or Docker option. There is **no relay/TURN** — when NAT traversal fails, the user gets an actionable error.

## What to read first

In order:

1. `docs/design/project-overview.md` — vision, users, goals, non-goals, design principles, environment.
2. The current phase section in `docs/plan/implementation-plan.md` — what is in scope right now.
3. `docs/design/architecture.md` — components, transport, NAT strategy, pluggable interfaces, protocol versioning, repository layout.
4. `docs/design/data-model.md` — durable entities and their per-backend mappings.
5. `docs/plan/open-questions.md` — what is not yet decided (and the default assumptions to use until it is).
6. `docs/plan/task-breakdown.md` — when the current phase has been broken down to tasks.
7. `docs/plan/acceptance-criteria.md` — how you'll know the phase is done.
8. `docs/design/glossary.md` — when you encounter an unfamiliar term.

## Source of truth and precedence

When two docs say different things, treat them in this order, highest first:

1. **`docs/design/project-overview.md` and `docs/design/architecture.md`** for vision, principles, and structural decisions.
2. **`docs/design/product-spec.md`** for what the product does and does not do.
3. **`docs/plan/implementation-plan.md`** for phase scope and ordering.
4. **`docs/design/data-model.md`** for durable entities.
5. **`docs/plan/task-breakdown.md`** for the next concrete step.
6. **`docs/plan/acceptance-criteria.md`** for done-criteria.
7. **`docs/plan/open-questions.md`** for items that are explicitly unresolved.

If a conflict exists, **propose a doc update** as part of your plan rather than picking silently.

## Working mode

### Default to Plan Mode at the start of every phase

Even if the user just says "let's do Phase 2", you produce a plan covering scope, design decisions, open questions, and validation **before** writing code. Wait for the plan to be reviewed. Begin implementation only after the plan is approved.

### Ask design questions liberally during planning

Lines like "I see two reasonable approaches here, which do you prefer?" or "this conflicts with the Phase 4 description in `docs/plan/implementation-plan.md` — should we revise the plan or change my approach?" are exactly what is wanted. Treat decisions in the docs as **current intent, not frozen specs** — they evolve as each phase teaches us something.

### Keep changes scoped to the current phase

Don't preemptively refactor for Phase N+1. Don't preemptively add features that the current phase does not need. Don't introduce abstractions beyond what the task requires. The brief that produced these docs explicitly preferred small reviewable steps over feature completeness, especially in Phase 1.

### Update the docs when assumptions change

If your phase's work reveals that an earlier doc is wrong or stale, **propose an edit to the relevant doc as part of that phase's plan**. Don't leave the codebase and the docs out of sync.

### Surface risks early

If you see something in a later phase that the current phase locks us out of, say so now. The canonical example is Phase 1's `core/transport/` external-`net.PacketConn` requirement: skipping it in Phase 1 would force a rewrite in Phase 3.

### Default to small, reviewable steps

Especially in Phase 1, prefer "smallest thing that works end-to-end" over feature completeness. A working two-machine demo with hardcoded values beats a half-implemented feature-complete tree.

## Boundaries and rules

These are non-negotiable. Violating them is a regression, even if the current phase does not exercise them.

- **No relay/TURN.** Don't introduce code paths that relay QUIC traffic. NAT traversal failures fail loudly.
- **Signaling never carries data.** Command bytes never flow through the signaling server.
- **No Firebase types in `core/`.** All Firebase/Firestore symbols live under `core/auth/firebase/` or `core/store/firestore/`. Importing the Firebase SDK from anywhere else is a layering violation.
- **No per-connection state in package globals.** Per-connection state is owned by structs you can construct and tear down, not by package-level variables.
- **Pluggable interfaces are in their final shape from Phase 1.** `auth.Provider` and `store.Store` exist with their final shape from day one, even though only `none` and `memory` ship initially.
- **Protocol stability.** Once Phase 1 ships, breaking changes require bumping `protocol_version`. Optional features go through `capabilities` strings on Hello messages.
- **mTLS-derived identity.** `device_id = base32(sha256(publicKey)[:16])`. The same key is identity and TLS credential.
- **Cost discipline (Firebase mode).** ≤ ~5 Firestore reads + ~2 writes per connection lifecycle. No real-time listeners for signaling. Client-side caching for device info and public keys.
- **Apache 2.0 only.** All first-party code is Apache 2.0. Don't pull in dependencies whose license is incompatible.

## Build order and starting a phase

When the user says something like "let's start Phase N":

1. **Re-read** `docs/design/project-overview.md`, the relevant phase section in `docs/plan/implementation-plan.md`, and the cross-phase invariants.
2. **Stay in Plan Mode.** Do not begin implementation work yet.
3. **Produce a plan** covering: scope, design decisions, open questions, deviations from the docs (if any, with justification), and a validation procedure that maps to `docs/plan/acceptance-criteria.md`.
4. **List open questions.** Pull from `docs/plan/open-questions.md` plus anything new you noticed.
5. **Wait for review.** Begin implementation only after the plan is approved.
6. **After the phase**, propose updates to the relevant docs if assumptions changed. Move resolved items out of `docs/plan/open-questions.md` into the doc that owns the answer.

## Common pitfalls to avoid

- Over-broadening Phase 1 by adding signaling, auth, or NAT code "since it's coming anyway." Don't. Phase 1 is the smallest thing that works on a same-LAN connection.
- Adding `os.Exit` / `log.Fatal` / panic-as-control-flow in library packages. Library code returns errors; binaries decide what to do with them.
- Adding goroutines without lifecycle ownership. Every goroutine has a documented owner that knows how to stop it.
- Treating the proposed repository layout as frozen. The brief that produced `docs/design/architecture.md` framed the layout as a starting proposal; refining it during Phase 1 planning is expected.
- Treating the protobuf schema as frozen post-Phase-1. It is fixed for `protocol_version=1`; new versions exist for new schemas. Capabilities, not silent edits, handle additive changes.
- Treating `docs/plan/open-questions.md` as a passive log. When a question is answered, move it.

## Recurring themes (read this before every phase)

These are mentioned in nearly every phase's plan. They are repeated here because they are easy to forget.

- **Open-source readiness.** Even Phase 1 code is written assuming public review.
- **Pluggability.** Auth and store are pluggable from day one; the *interfaces* are in place even when the trivial implementation is the only one shipping.
- **Privacy / threat model.** The signaling server operator cannot see command content. mTLS-derived identity prevents impersonation.
- **Cost discipline (Firebase mode).** Each connection lifecycle is cheap. Use client-side caches. No real-time listeners for signaling.
- **Protocol stability.** `protocol_version` is the breaking-change knob. `capabilities` strings handle additive changes.

## When in doubt

Ask the user. The docs encode current intent, but the user is the source of truth for intent. Phrasing of the form "I'd like to do X. Two reasonable approaches are A and B. A is better for reason R; B is better for reason S. Do you want me to use A?" beats silently picking.
