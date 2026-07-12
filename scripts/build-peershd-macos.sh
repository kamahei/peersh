#!/usr/bin/env bash
# Build a distribution-ready macOS peershd host with embedded Firebase
# defaults — the macOS analogue of build-peershd-distrib.sh (Windows).
#
# End users running the resulting binary won't need to pass any
# -firebase-* / -google-* flags. The daemon spawns the user's login shell
# (zsh/bash) under a forkpty pseudo-terminal, so a Mac becomes a peersh host
# controllable from the iOS/Android clients exactly like a Windows host.
#
# Required env (read from local/peershd-build.env if present; see
# scripts/peershd-build.env.example):
#   PEERSH_BUILD_FIREBASE_PROJECT_ID, PEERSH_BUILD_FIREBASE_API_KEY,
#   PEERSH_BUILD_GOOGLE_CLIENT_ID, PEERSH_BUILD_GOOGLE_CLIENT_SECRET
#
# Usage:
#   scripts/build-peershd-macos.sh            # native arch → local/peershd
#   scripts/build-peershd-macos.sh universal  # arm64+amd64 fat binary
#
# Output: local/peershd (Mach-O).
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f local/peershd-build.env ]]; then
  set -a
  # shellcheck disable=SC1091
  source local/peershd-build.env
  set +a
fi

: "${PEERSH_BUILD_FIREBASE_PROJECT_ID:?required}"
: "${PEERSH_BUILD_FIREBASE_API_KEY:?required}"
: "${PEERSH_BUILD_GOOGLE_CLIENT_ID:?required}"
: "${PEERSH_BUILD_GOOGLE_CLIENT_SECRET:?required}"
: "${PEERSH_BUILD_FIREBASE_REGION:=asia-northeast1}"
: "${PEERSH_BUILD_FIREBASE_RTDB_REGION:=asia-southeast1}"
: "${PEERSH_BUILD_SIGNALING_URL:=}"
: "${PEERSH_BUILD_VERSION:=dev-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
: "${PEERSH_BUILD_UPDATE_REPO:=}"

LDFLAGS="
  -X 'main.embeddedFirebaseAPIKey=${PEERSH_BUILD_FIREBASE_API_KEY}'
  -X 'main.embeddedFirebaseProjectID=${PEERSH_BUILD_FIREBASE_PROJECT_ID}'
  -X 'main.embeddedFirebaseRegion=${PEERSH_BUILD_FIREBASE_REGION}'
  -X 'main.embeddedFirebaseRtdbRegion=${PEERSH_BUILD_FIREBASE_RTDB_REGION}'
  -X 'main.embeddedSignalingURL=${PEERSH_BUILD_SIGNALING_URL}'
  -X 'main.embeddedGoogleClientID=${PEERSH_BUILD_GOOGLE_CLIENT_ID}'
  -X 'main.embeddedGoogleClientSecret=${PEERSH_BUILD_GOOGLE_CLIENT_SECRET}'
  -X 'main.embeddedVersion=${PEERSH_BUILD_VERSION}'
  -X 'main.embeddedUpdateRepo=${PEERSH_BUILD_UPDATE_REPO}'
"

# The host is pure Go (quic-go, firebase admin, creack/pty all cgo-free), so
# CGO_ENABLED=0 keeps cross-arch builds working without a C toolchain.
build_one() { # $1=arch  $2=out
  GOOS=darwin GOARCH="$1" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$2" ./windows/cmd/peershd
}

mkdir -p local
if [[ "${1:-native}" == "universal" ]]; then
  build_one arm64 local/peershd.arm64
  build_one amd64 local/peershd.amd64
  lipo -create -output local/peershd local/peershd.arm64 local/peershd.amd64
  rm -f local/peershd.arm64 local/peershd.amd64
else
  build_one "$(go env GOARCH)" local/peershd
fi

ls -la local/peershd
echo "Built local/peershd (macOS host) with embedded Firebase defaults."
