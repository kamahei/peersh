# peersh documentation

The top-level `README.md` is the user-facing entry point; this folder holds everything else.

## Structure

```
docs/
├── design/         Project vision, product spec, architecture, data model, glossary.
├── deploy/         Operator-side deployment walkthroughs (Cloud Run, Render, self-host, Firebase).
├── store/          Play Store submission drafts (only relevant when publishing to a store).
├── build.md        How to build every component from source.
├── user-manual.md  Mobile-app user guide.
└── backup.md       Backup of git-ignored local secrets (for moving to a new build machine).
```

## Read order for a new user

1. Top-level [`README.md`](../README.md) — what the project is, fastest quick-start.
2. [`build.md`](build.md) — build everything from source.
3. [`deploy/README.md`](deploy/README.md) — pick the signaling deployment that fits.
4. [`user-manual.md`](user-manual.md) — using the mobile app day-to-day.
5. [`backup.md`](backup.md) — what to back up so you can rebuild on a fresh machine.

## Read order for a new contributor

1. [`design/project-overview.md`](design/project-overview.md) — vision, users, goals, non-goals, design principles.
2. [`design/architecture.md`](design/architecture.md) — components, transport, NAT strategy, pluggable interfaces, protocol versioning, repository layout.
3. [`design/product-spec.md`](design/product-spec.md) — capabilities, user journeys, NFRs, out-of-scope.
4. [`design/data-model.md`](design/data-model.md) — durable entities and per-backend mapping.
5. [`design/glossary.md`](design/glossary.md) — terms used across the docs and code.
6. [`../CONTRIBUTING.md`](../CONTRIBUTING.md) and [`../AGENTS.md`](../AGENTS.md) — conventions + AI-agent operating rules.
