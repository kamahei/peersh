#!/usr/bin/env bash
# Build peersh-cli locally, embedding the same Firebase / signaling
# defaults that build-peershd-distrib.sh reads from
# local/peershd-build.env. End users running the resulting binary
# won't need to pass -signaling, -firebase-project, or
# -firebase-api-key to use Firebase mode out of the box.
#
# Required env vars in local/peershd-build.env (the same file peershd
# uses; see scripts/peershd-build.env.example):
#   PEERSH_BUILD_SIGNALING_URL          (used by both PSK and Firebase)
#   PEERSH_BUILD_FIREBASE_PROJECT_ID    (Firebase mode)
#   PEERSH_BUILD_FIREBASE_API_KEY       (Firebase mode)
#   PEERSH_BUILD_FIREBASE_REGION        (Firebase mode; defaults to asia-northeast1)
#   PEERSH_BUILD_FIREBASE_RTDB_REGION   (Firebase mode; defaults to asia-southeast1; used for host auto-discovery)
#   PEERSH_BUILD_GOOGLE_CLIENT_ID       (Firebase mode, -firebase-login)
#   PEERSH_BUILD_GOOGLE_CLIENT_SECRET   (Firebase mode, -firebase-login)
#
# Output: local/peersh-cli (or local/peersh-cli.exe when GOOS=windows).
#
# Optional env vars:
#   GOOS, GOARCH  override target OS / arch (default: host's GOOS/GOARCH).

set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f local/peershd-build.env ]]; then
  set -a
  # shellcheck disable=SC1091
  source local/peershd-build.env
  set +a
fi

: "${PEERSH_BUILD_SIGNALING_URL:=}"
: "${PEERSH_BUILD_FIREBASE_PROJECT_ID:=}"
: "${PEERSH_BUILD_FIREBASE_API_KEY:=}"
: "${PEERSH_BUILD_FIREBASE_REGION:=asia-northeast1}"
: "${PEERSH_BUILD_FIREBASE_RTDB_REGION:=asia-southeast1}"
: "${PEERSH_BUILD_GOOGLE_CLIENT_ID:=}"
: "${PEERSH_BUILD_GOOGLE_CLIENT_SECRET:=}"

target_goos="${GOOS:-$(go env GOOS)}"
target_goarch="${GOARCH:-$(go env GOARCH)}"

out="local/peersh-cli"
if [[ "$target_goos" == "windows" ]]; then
  out="local/peersh-cli.exe"
fi

GOOS="$target_goos" GOARCH="$target_goarch" \
  go build -trimpath \
  -ldflags "
    -X 'main.embeddedSignalingURL=${PEERSH_BUILD_SIGNALING_URL}'
    -X 'main.embeddedFirebaseProjectID=${PEERSH_BUILD_FIREBASE_PROJECT_ID}'
    -X 'main.embeddedFirebaseAPIKey=${PEERSH_BUILD_FIREBASE_API_KEY}'
    -X 'main.embeddedFirebaseRegion=${PEERSH_BUILD_FIREBASE_REGION}'
    -X 'main.embeddedFirebaseRtdbRegion=${PEERSH_BUILD_FIREBASE_RTDB_REGION}'
    -X 'main.embeddedGoogleClientID=${PEERSH_BUILD_GOOGLE_CLIENT_ID}'
    -X 'main.embeddedGoogleClientSecret=${PEERSH_BUILD_GOOGLE_CLIENT_SECRET}'
  " \
  -o "$out" ./cli/cmd/peersh-cli

ls -la "$out"
echo "Built $out (GOOS=$target_goos GOARCH=$target_goarch, signaling=${PEERSH_BUILD_SIGNALING_URL}, firebase_project=${PEERSH_BUILD_FIREBASE_PROJECT_ID})."
