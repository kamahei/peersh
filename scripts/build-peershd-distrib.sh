#!/usr/bin/env bash
# Build a distribution-ready peershd.exe with embedded Firebase
# defaults. End users running the resulting binary won't need to pass
# any -firebase-* / -google-* flags.
#
# Required env vars (read from `local/peershd-build.env` if present;
# see scripts/peershd-build.env.example):
#   PEERSH_BUILD_FIREBASE_PROJECT_ID
#   PEERSH_BUILD_FIREBASE_API_KEY
#   PEERSH_BUILD_FIREBASE_REGION       (optional, default asia-northeast1)
#   PEERSH_BUILD_SIGNALING_URL         (optional)
#   PEERSH_BUILD_GOOGLE_CLIENT_ID
#   PEERSH_BUILD_GOOGLE_CLIENT_SECRET
#
# Output:
#   local/peershd.exe (Windows binary; cross-compiled from any host).

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
: "${PEERSH_BUILD_SIGNALING_URL:=}"

GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "
    -X 'main.embeddedFirebaseAPIKey=${PEERSH_BUILD_FIREBASE_API_KEY}'
    -X 'main.embeddedFirebaseProjectID=${PEERSH_BUILD_FIREBASE_PROJECT_ID}'
    -X 'main.embeddedFirebaseRegion=${PEERSH_BUILD_FIREBASE_REGION}'
    -X 'main.embeddedSignalingURL=${PEERSH_BUILD_SIGNALING_URL}'
    -X 'main.embeddedGoogleClientID=${PEERSH_BUILD_GOOGLE_CLIENT_ID}'
    -X 'main.embeddedGoogleClientSecret=${PEERSH_BUILD_GOOGLE_CLIENT_SECRET}'
  " \
  -o local/peershd.exe ./windows/cmd/peershd

ls -la local/peershd.exe
echo "Built local/peershd.exe with embedded Firebase defaults."
