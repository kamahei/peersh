#!/usr/bin/env bash
# Install peershd as a per-user macOS LaunchAgent so this Mac becomes a peersh
# host that starts at login and can be controlled from the iOS/Android clients.
#
# What it does:
#   1. builds peershd with embedded Firebase config (scripts/build-peershd-macos.sh)
#   2. copies it to a stable path (~/.local/bin/peershd by default)
#   3. bootstraps Firebase auth once (browser sign-in) if no refresh token yet
#   4. registers + starts the LaunchAgent (~/Library/LaunchAgents/peershd.plist,
#      RunAtLoad + KeepAlive) via kardianos/service
#
# Manage it afterwards:
#   ~/.local/bin/peershd -service-status
#   ~/.local/bin/peershd -stop
#   scripts/uninstall-peershd-macos.sh   (or: ~/.local/bin/peershd -uninstall)
#
# Env overrides:
#   PEERSH_BIN_DIR   install dir for the binary (default ~/.local/bin)
#   PEERSH_PAIR_CODE if set, bootstrap auth with this 6-digit pair code from the
#                    mobile app's "Pair PC" screen instead of the browser flow
set -euo pipefail
cd "$(dirname "$0")/.."

BIN_DIR="${PEERSH_BIN_DIR:-$HOME/.local/bin}"
BIN="$BIN_DIR/peershd"
TOKEN="${HOME}/.local/share/peersh/firebase-refresh-token.txt"

echo ">> building peershd (embedded Firebase)"
bash scripts/build-peershd-macos.sh

echo ">> installing binary to $BIN"
mkdir -p "$BIN_DIR"
cp local/peershd "$BIN"
chmod +x "$BIN"

# peershd needs a persisted Firebase refresh token to run unattended under
# launchd. Bootstrap it once (skip if it already exists).
if [[ -f "$TOKEN" ]]; then
  echo ">> Firebase refresh token already present ($TOKEN); skipping auth bootstrap"
elif [[ -n "${PEERSH_PAIR_CODE:-}" ]]; then
  echo ">> bootstrapping Firebase auth with pair code"
  "$BIN" -pair-code "$PEERSH_PAIR_CODE" -firebase-login-only
else
  echo ">> bootstrapping Firebase auth (a browser window will open to sign in)"
  "$BIN" -firebase-login -firebase-login-only
fi

echo ">> registering + starting the LaunchAgent (~/Library/LaunchAgents/peershd.plist)"
"$BIN" -install
"$BIN" -start

echo
echo "Done — this Mac is now a peersh host and will start at login."
echo "  status:    $BIN -service-status"
echo "  logs:      log show --predicate 'process == \"peershd\"' --last 5m"
echo "  uninstall: scripts/uninstall-peershd-macos.sh"
