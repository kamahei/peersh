# peersh documentation

This folder is the canonical project documentation. The top-level `README.md` and `AGENTS.md` are entry points; everything else lives here.

## Structure

```
docs/
├── design/    Project vision, product spec, architecture, data model, glossary.
├── plan/      Phase roadmap, acceptance criteria, task breakdown, open questions, AI-agent guide.
└── deploy/    Operations and per-platform deploy walkthroughs.
```

## Read order for a new contributor

1. [`design/project-overview.md`](design/project-overview.md) — vision, users, goals, non-goals, design principles, environment.
2. [`design/architecture.md`](design/architecture.md) — components, transport, NAT strategy, signaling protocol, pluggable interfaces, protocol versioning, repository layout.
3. [`design/product-spec.md`](design/product-spec.md) — capabilities, user journeys, NFRs, explicit out-of-scope.
4. [`design/data-model.md`](design/data-model.md) — durable entities (Device, User, Pairing, Session, PSKRecord) and per-backend mappings.
5. [`plan/implementation-plan.md`](plan/implementation-plan.md) — the seven-phase roadmap.
6. [`plan/task-breakdown.md`](plan/task-breakdown.md) — each phase's tasks with shipped status.
7. [`plan/acceptance-criteria.md`](plan/acceptance-criteria.md) — cross-phase invariants and per-phase done criteria.
8. [`plan/open-questions.md`](plan/open-questions.md) — what is not yet decided, with current default assumptions.
9. [`design/glossary.md`](design/glossary.md) — terms used across the docs and code.

## For AI agents working in this repository

[`plan/ai-implementation-guide.md`](plan/ai-implementation-guide.md) is the operating manual: read order, source-of-truth precedence, working mode (Plan-Mode-per-phase), and durable boundaries. The companion [`AGENTS.md`](../AGENTS.md) at the repo root is the short-form version that the harness loads automatically.

## For operators deploying peersh

[`deploy/README.md`](deploy/README.md) is the deploy hub. From there:

- [`deploy/cloud-run.md`](deploy/cloud-run.md) — GCP Cloud Run (no-fixed-fee, pay-per-use).
- [`deploy/render-com.md`](deploy/render-com.md) — Render.com Blueprint (zero-config GitHub deploy).
- [`deploy/self-hosting.md`](deploy/self-hosting.md) — generic Docker/VPS plus all target-agnostic operations (TLS, PSK lifecycle, metrics, troubleshooting).
- [`deploy/firebase.md`](deploy/firebase.md) — Phase 5 Firestore + Cloud Functions for the official hosted option.
