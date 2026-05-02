#!/usr/bin/env bash
# Build a distribution-ready Android release APK.
#
# The repo ships placeholder Firebase configs at:
#   app/lib/firebase_options.dart        (throws UnsupportedError at runtime)
#   app/android/app/google-services.json (project_id "peersh-firebase-placeholder")
#   app/firebase.json                    (placeholder ids)
#
# Operator-specific real configs live in local/ (gitignored):
#   local/firebase_options.dart.real
#   local/google-services.json.real
#   local/app-firebase.json.real
#
# This script swaps placeholder -> real, runs flutter build, and restores
# the placeholders via git checkout so the secrets are never accidentally
# committed.
#
# Output: app/build/app/outputs/flutter-apk/app-release.apk
#
# Prerequisites:
#   - flutter installed and on PATH (>= 3.24)
#   - JDK 17 on PATH or JAVA_HOME set
#   - mobile-core/peersh.aar built (run scripts/build-mobile-core.sh android)
#   - local/*.real files present
#
# Note: when app/android/key.properties is absent, the release build
# falls back to the debug keystore (sideload-only; not Play Store
# acceptable). See app/android/key.properties.example.

set -euo pipefail
cd "$(dirname "$0")/.."

real_files=(
  "local/firebase_options.dart.real:app/lib/firebase_options.dart"
  "local/google-services.json.real:app/android/app/google-services.json"
  "local/app-firebase.json.real:app/firebase.json"
)

for pair in "${real_files[@]}"; do
  src="${pair%%:*}"
  if [[ ! -f "$src" ]]; then
    echo "ERROR: missing $src — copy your FlutterFire output here first." >&2
    exit 1
  fi
done

restore() {
  echo ">> restoring placeholder Firebase configs"
  for pair in "${real_files[@]}"; do
    dst="${pair##*:}"
    git checkout -- "$dst" 2>/dev/null || true
  done
}
trap restore EXIT

echo ">> swapping placeholder -> real Firebase configs"
for pair in "${real_files[@]}"; do
  src="${pair%%:*}"
  dst="${pair##*:}"
  cp "$src" "$dst"
done

echo ">> flutter build apk --release"
( cd app && flutter build apk --release )

echo
echo "Built app/build/app/outputs/flutter-apk/app-release.apk"
ls -la app/build/app/outputs/flutter-apk/app-release.apk
