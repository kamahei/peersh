/// Output / lifecycle events from a single PTY session, forwarded by the
/// platform EventChannel.
///
/// PTY events live alongside one-shot Exec events (see session_event.dart)
/// on the same EventChannel; the platform side tags each event's payload
/// with either a `sessionId` (Exec) or a `ptyId` (PTY) so Dart can
/// dispatch.
sealed class PtyEvent {
  const PtyEvent(this.ptyId);
  final int ptyId;

  /// Returns null if the raw map is not a PTY event (e.g. an Exec event
  /// landed on the same broadcast stream and should be ignored by PTY
  /// listeners).
  static PtyEvent? fromMap(Map<dynamic, dynamic> m) {
    final type = m['type'] as String?;
    if (type == null) return null;
    if (type != 'ptyData' && type != 'ptyExit') return null;
    final id = (m['ptyId'] as num?)?.toInt();
    if (id == null) return null;
    switch (type) {
      case 'ptyData':
        return PtyDataEvent(id, _bytes(m['data']));
      case 'ptyExit':
        return PtyExitEvent(
          id,
          (m['exitCode'] as num?)?.toInt() ?? -1,
          m['error'] as String? ?? '',
        );
    }
    return null;
  }
}

/// Raw bytes from the child's pseudo-console (already merged stdout/stderr).
class PtyDataEvent extends PtyEvent {
  const PtyDataEvent(super.ptyId, this.data);
  final List<int> data;
}

/// Terminal event: emitted exactly once when the child process exits
/// or the stream tears down.
class PtyExitEvent extends PtyEvent {
  const PtyExitEvent(super.ptyId, this.exitCode, this.error);
  final int exitCode;
  final String error;

  bool get isError => error.isNotEmpty;
}

List<int> _bytes(Object? raw) {
  if (raw == null) return const <int>[];
  if (raw is List<int>) return raw;
  if (raw is List) return List<int>.from(raw.cast<int>());
  return const <int>[];
}
