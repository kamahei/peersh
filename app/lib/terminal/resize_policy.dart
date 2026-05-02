// Keep this policy narrow: peersh spawns Windows shells (pwsh,
// powershell, cmd) plus user-supplied executables. The PowerShell
// formatter caches the initial console width on startup, so a too-narrow
// PTY makes Get-Process / Format-Table / table output truncate every
// column. We work around that by pegging the remote PTY at >= 120 cols
// when the local user is in wrap mode and the host shell is pwsh-like.

/// Fixed remote column count used for scroll mode and as the floor for
/// wrap mode against PowerShell.
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
int remoteColsFor({
  required String shell,
  required bool lineWrap,
  required int visibleCols,
}) {
  final cols = visibleCols.clamp(1, 500).toInt();
  if (lineWrap && isPowerShellShell(shell)) {
    return cols < kHorizontalScrollCols ? kHorizontalScrollCols : cols;
  }
  return cols;
}
