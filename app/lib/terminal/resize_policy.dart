// Keep this policy narrow: peersh spawns Windows shells (pwsh,
// powershell, cmd) plus user-supplied executables. The PowerShell
// formatter caches the initial console width on startup, so a too-narrow
// PTY makes Get-Process / Format-Table / table output truncate every
// column. We work around that by pegging the remote PTY at >= 120 cols
// when the local user is in wrap mode and the host shell is pwsh-like.

/// Fallback remote column count used when no user setting has loaded
/// yet. Live code should pass the user-configured value via the
/// `cols` parameter on [remoteColsFor]; this constant only exists for
/// the legacy `_ScrollBody` initial layout pass and tests.
const int kHorizontalScrollCols = 120;

/// Returns true when [shell] looks like a PowerShell variant.
///
/// Accepts plain names ("pwsh", "powershell"), absolute paths, and
/// .exe-suffixed forms.
bool isPowerShellShell(String shell) {
  final normalized = shell.replaceAll('\\', '/').toLowerCase();
  final executable = normalized.split('/').last;
  final name = executable.endsWith('.exe')
      ? executable.substring(0, executable.length - 4)
      : executable;
  return name == 'pwsh' ||
      name == 'powershell' ||
      name.isEmpty ||
      name == 'auto';
}

/// Computes the remote PTY's `cols` given the local visible cell count
/// and the current wrap/scroll mode.
///
/// [terminalCols] is the user-configured fixed column count (used as
/// the scroll-mode width and as the wrap-mode floor against PowerShell).
/// Defaults to [kHorizontalScrollCols] for callers that haven't been
/// updated yet.
int remoteColsFor({
  required String shell,
  required bool lineWrap,
  required int visibleCols,
  int terminalCols = kHorizontalScrollCols,
}) {
  final cols = visibleCols.clamp(1, 500).toInt();
  if (lineWrap && isPowerShellShell(shell)) {
    return cols < terminalCols ? terminalCols : cols;
  }
  return cols;
}
