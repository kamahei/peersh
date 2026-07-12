#!/usr/bin/env bash
# Remove the peershd macOS LaunchAgent installed by install-peershd-macos.sh.
# Leaves the binary and the persisted Firebase refresh token in place.
set -uo pipefail

BIN_DIR="${PEERSH_BIN_DIR:-$HOME/.local/bin}"
BIN="$BIN_DIR/peershd"
PLIST="$HOME/Library/LaunchAgents/peershd.plist"

if [[ -x "$BIN" ]]; then
  "$BIN" -stop 2>/dev/null || true
  "$BIN" -uninstall 2>/dev/null || true
fi

# Fallback: unload + remove the plist directly if the binary is gone.
if [[ -f "$PLIST" ]]; then
  launchctl unload "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"
fi

echo "peershd LaunchAgent removed."
echo "  (binary $BIN and ~/.local/share/peersh/ left in place; delete manually if desired.)"
