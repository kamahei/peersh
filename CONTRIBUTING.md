# Contributing to peersh

Thanks for considering a contribution. peersh is Apache-2.0 licensed and
welcomes patches, bug reports, and ideas.

## Quick orientation

- `core/` — shared Go library (transport, signaling client, auth, store, punching, wire framing).
- `server/` — `peersh-signaling` (Go).
- `windows/` — `peershd` Windows host (Go).
- `cli/` — `peersh-cli` developer client (Go).
- `mobile-core/` — gomobile-friendly Go shim used by the mobile app.
- `app/` — Flutter app (Dart).
- `proto/` — protobuf source. Generated Go lands at `core/protocol/...`.
- `firebase/` — Firebase project artefacts (Cloud Functions, Firestore rules).
- `docs/` — design + operator + user documentation. Index: `docs/README.md`.

`AGENTS.md` is the durable instruction file for AI agents working on
the codebase. Humans benefit from reading it too — it spells out the
project's invariants (no relay/TURN, signaling never sees data, etc.).

## Before you start

- Read `docs/design/project-overview.md` for the design principles.
- Read the relevant `docs/design/architecture.md` section.
- Run `go test -count=1 ./...` and `cd app && flutter analyze` to
  baseline before changes.

## Building

```sh
# Go workspace tests
go test -count=1 ./core/... ./server/... ./windows/... ./cli/... ./mobile-core/...

# Server binary
go build -o bin/peersh-signaling ./server/cmd/peersh-signaling

# Windows host binary
go build -o bin/peershd.exe ./windows/cmd/peershd

# Mobile-core AAR (Android)
scripts/build-mobile-core.sh android   # macOS/Linux
scripts\build-mobile-core.cmd          # Windows

# Flutter app analyze + APK
cd app
flutter pub get
flutter analyze
flutter build apk --debug
```

The mobile AAR is gitignored; CI rebuilds it.

## Coding conventions

- **Go**: standard library + a small set of vetted modules (quic-go,
  pion/stun, Firebase Admin, Cloud Firestore client, prometheus,
  modernc.org/sqlite, nhooyr/websocket, BurntSushi/toml,
  google.golang.org/protobuf). Avoid pulling in new heavy deps without
  discussion.
- **Dart**: Riverpod for state, `flutter_secure_storage` for secrets,
  `http` for plain HTTP. Stay on Flutter stable. Material 3 theming.
- **Naming**: package directories match their Go package name. Dart
  files use snake_case.
- **Comments**: prefer them where the *why* is non-obvious. Don't
  paraphrase code that already speaks for itself.
- **Tests**: every new Go package ships unit tests. End-to-end coverage
  lives at the boundaries (`server/ws/e2e_test.go`,
  `server/ws/phase3_e2e_test.go`).

## Project invariants

These are non-negotiable and apply to every change. They're also captured
in `AGENTS.md`:

- No relay / TURN. NAT-traversal failure surfaces an actionable error.
- The signaling server never sees PowerShell command content.
- mTLS-derived identity. `device_id = base32(sha256(publicKey)[:16])`.
- Pluggable interfaces stay in their final shape; adding a new
  provider/store means implementing the interface, not editing `core/`.
- No Firebase types in `core/` outside `core/auth/firebase/` and
  `core/store/firestore/`.
- No per-connection state in package globals.
- Protocol stability. Breaking changes require bumping
  `protocol_version`. Optional features go through `capabilities`.
- Cost discipline (Firebase mode). ≤ ~5 reads + ~2 writes per
  connection lifecycle.
- Apache 2.0 only. Don't add dependencies whose license is incompatible.

## Pull requests

1. Open or comment on an issue first if the change is non-trivial.
2. Fork → feature branch.
3. Keep commits small and reviewable.
4. Run the test suite + linters locally.
5. Open the PR with a clear description.
6. The Android / peershd build workflows under `.github/workflows/` can
   be invoked manually if you want CI artefacts for the change.

## Reporting bugs

Open an issue with:

- peersh version (`peershd version` or git SHA).
- Server-side: which `auth_provider` + `store_backend` you ran with.
- Client-side: Android / iOS version, app build.
- Reproduction steps and expected vs. actual behaviour.

For security issues, use the disclosure path in `SECURITY.md` instead
of a public issue.

## Release flow

(Mostly relevant for maintainers.)

1. Run the full test suite plus `flutter build apk --release`.
2. Tag a release as `v<semver>` and push the tag — `.github/workflows/release.yml`
   builds peershd Windows binaries + Android APK + .sha256 sidecars and
   attaches them to a GitHub Release.
3. Update `firebase/` artefacts if any Cloud Function or rule changed
   (`firebase deploy --only firestore:rules,functions`).
