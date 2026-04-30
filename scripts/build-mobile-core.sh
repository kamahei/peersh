#!/usr/bin/env bash
# Build mobile-core artifacts for Flutter consumption.
#
#   Android: app/android/app/libs/peersh.aar
#   iOS:     app/ios/Frameworks/peersh.xcframework  (requires macOS / Xcode)
#
# Prerequisites:
#   - go install golang.org/x/mobile/cmd/gomobile@latest
#   - gomobile init   (one-time, sets up the bind toolchain)
#   - ANDROID_HOME / ANDROID_NDK_HOME pointing at a real SDK + NDK
#
# Usage:
#   scripts/build-mobile-core.sh                 # both targets if available
#   scripts/build-mobile-core.sh android         # Android-only
#   scripts/build-mobile-core.sh ios             # iOS-only
set -euo pipefail
cd "$(dirname "$0")/.."

target="${1:-all}"
mkdir -p app/android/app/libs app/ios/Frameworks

if [[ "$target" == "android" || "$target" == "all" ]]; then
  echo ">> gomobile bind -target=android"
  gomobile bind \
    -target=android -androidapi 21 \
    -o app/android/app/libs/peersh.aar \
    github.com/peersh/peersh/mobile-core
  ls -la app/android/app/libs/peersh.aar
fi

if [[ "$target" == "ios" || "$target" == "all" ]]; then
  if [[ "$(uname -s)" == "Darwin" ]]; then
    echo ">> gomobile bind -target=ios"
    gomobile bind \
      -target=ios \
      -o app/ios/Frameworks/peersh.xcframework \
      github.com/peersh/peersh/mobile-core
  else
    echo "skipping iOS bind: requires macOS"
  fi
fi
