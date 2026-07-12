#!/usr/bin/env bash
# Build mobile-core artifacts for Flutter consumption.
#
#   Android:      app/android/app/libs/peersh.aar
#   Apple (iOS):  app/shared/apple/Frameworks/peersh.xcframework  (requires macOS / Xcode)
#                 One xcframework carries the iphoneos + iphonesimulator slices,
#                 embedded by the iOS Runner. (macOS is a peersh host, not a
#                 Flutter client, so there is no macOS client slice.)
#
# Prerequisites:
#   - go install golang.org/x/mobile/cmd/gomobile@latest
#   - gomobile init   (one-time, sets up the bind toolchain)
#   - ANDROID_HOME / ANDROID_NDK_HOME pointing at a real SDK + NDK (android only)
#   - Xcode + command-line tools (apple only)
#
# Usage:
#   scripts/build-mobile-core.sh                 # all available targets
#   scripts/build-mobile-core.sh android         # Android-only
#   scripts/build-mobile-core.sh apple           # iOS + macOS (one xcframework)
#   scripts/build-mobile-core.sh ios             # alias for apple
#   scripts/build-mobile-core.sh macos           # alias for apple
set -euo pipefail
cd "$(dirname "$0")/.."

target="${1:-all}"
apple_out="app/shared/apple/Frameworks/peersh.xcframework"
mkdir -p app/android/app/libs "$(dirname "$apple_out")"

if [[ "$target" == "android" || "$target" == "all" ]]; then
  echo ">> gomobile bind -target=android"
  gomobile bind \
    -target=android -androidapi 21 \
    -o app/android/app/libs/peersh.aar \
    github.com/peersh/peersh/mobile-core
  ls -la app/android/app/libs/peersh.aar
fi

# iOS and macOS share one xcframework. We always build both Apple slices so a
# `macos` build can never clobber the iOS slice (or vice versa) out of the
# combined artifact.
if [[ "$target" == "ios" || "$target" == "macos" || "$target" == "apple" || "$target" == "all" ]]; then
  if [[ "$(uname -s)" == "Darwin" ]]; then
    # iossimulator is a distinct slice from ios (device): without it,
    # `flutter run` on the iOS Simulator fails to link mobile-core. (No macOS
    # slice: macOS is a peersh *host*, not a Flutter client — only iOS/Android
    # clients consume mobile-core.)
    echo ">> gomobile bind -target=ios,iossimulator"
    rm -rf "$apple_out"
    gomobile bind \
      -target=ios,iossimulator \
      -o "$apple_out" \
      github.com/peersh/peersh/mobile-core
    # Sanity-check that the combined framework really carries both platforms.
    echo ">> slices in $apple_out:"
    /usr/libexec/PlistBuddy -c 'Print :AvailableLibraries' "$apple_out/Info.plist" 2>/dev/null \
      | grep -E 'SupportedPlatform|LibraryIdentifier' || ls -la "$apple_out"
  else
    echo "skipping Apple bind: requires macOS"
  fi
fi
