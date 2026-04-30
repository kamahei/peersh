/// A single output event from a peersh session, as forwarded by the
/// platform EventChannel.
sealed class SessionEvent {
  const SessionEvent(this.sessionId);
  final int sessionId;

  static SessionEvent fromMap(Map<dynamic, dynamic> m) {
    final id = (m['sessionId'] as num).toInt();
    final type = m['type'] as String;
    switch (type) {
      case 'stdout':
        return StdoutEvent(id, _bytes(m['data']));
      case 'stderr':
        return StderrEvent(id, _bytes(m['data']));
      case 'done':
        return DoneEvent(id, m['error'] as String? ?? '');
      default:
        return DoneEvent(id, 'unknown event type: $type');
    }
  }
}

class StdoutEvent extends SessionEvent {
  const StdoutEvent(super.sessionId, this.data);
  final List<int> data;
}

class StderrEvent extends SessionEvent {
  const StderrEvent(super.sessionId, this.data);
  final List<int> data;
}

class DoneEvent extends SessionEvent {
  const DoneEvent(super.sessionId, this.error);

  /// Empty on clean completion; non-empty for errors.
  final String error;

  bool get isError => error.isNotEmpty;
}

List<int> _bytes(Object? raw) {
  if (raw == null) return const <int>[];
  if (raw is List<int>) return raw;
  // Some platforms send Uint8List, others List<dynamic>; coerce.
  if (raw is List) return List<int>.from(raw.cast<int>());
  return const <int>[];
}
