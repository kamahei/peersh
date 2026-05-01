// MethodChannel + EventChannel bridge to mobile-core.
//
// Phase 4a shipped Version + Echo. Phase 4b adds the Session lifecycle:
// open (direct or signaling-mediated), exec (streaming output via the
// EventChannel), readFile (one-shot), close.

import 'package:flutter/services.dart';

import 'models/pty_event.dart';
import 'models/session_event.dart';

class PeershBridge {
  static const _control = MethodChannel('dev.peersh/bridge');
  static const _events = EventChannel('dev.peersh/session/events');

  /// Cached broadcast stream so PTY and Exec consumers share the same
  /// EventChannel subscription instead of competing on receiveBroadcastStream.
  static final Stream<dynamic> _eventsStream = _events.receiveBroadcastStream();

  /// Returns mobile-core's build identifier.
  Future<String> version() async {
    final v = await _control.invokeMethod<String>('version');
    return v ?? '';
  }

  /// Phase 4a's synchronous direct echo. Kept for the developer spike
  /// screen.
  Future<String> echo({required String addr, required String command}) async {
    final out = await _control.invokeMethod<String>('echo', {
      'addr': addr,
      'command': command,
    });
    return out ?? '';
  }

  /// Opens a direct QUIC session (no signaling). Returns the session id.
  Future<int> openDirectSession({required String addr}) async {
    final id = await _control.invokeMethod<int>('openDirectSession', {
      'addr': addr,
    });
    if (id == null) throw StateError('bridge: openDirectSession returned null');
    return id;
  }

  /// Opens a signaling-mediated session. Throws on signaling failure or
  /// NAT-traversal failure.
  Future<int> openSignalingSession({
    required String signaling,
    required String user,
    required String pskHex,
    required String targetDeviceId,
    String stunServer = 'stun.l.google.com:19302',
  }) async {
    final id = await _control.invokeMethod<int>('openSignalingSession', {
      'signaling': signaling,
      'user': user,
      'psk': pskHex,
      'target': targetDeviceId,
      'stun': stunServer,
    });
    if (id == null) throw StateError('bridge: openSignalingSession returned null');
    return id;
  }

  /// Runs command on the session. Output streams via [events] tagged with
  /// the session id. The future resolves when the platform-side worker
  /// has completed the call.
  Future<void> exec({required int sessionId, required String command}) async {
    await _control.invokeMethod<void>('exec', {
      'sessionId': sessionId,
      'command': command,
    });
  }

  /// One-shot: runs Get-Content -Raw -Encoding UTF8 -LiteralPath '<path>'
  /// and returns the captured stdout. On failure the returned string
  /// starts with "ERROR: ".
  Future<String> readFile({required int sessionId, required String path}) async {
    final out = await _control.invokeMethod<String>('readFile', {
      'sessionId': sessionId,
      'path': path,
    });
    return out ?? '';
  }

  /// Closes a session. Idempotent.
  Future<void> closeSession({required int sessionId}) async {
    await _control.invokeMethod<void>('closeSession', {
      'sessionId': sessionId,
    });
  }

  /// Opens an interactive PTY on an existing session. Returns a PTY id
  /// the caller uses for [ptyInput] / [ptyResize] / [closePty]. Output
  /// flows on [ptyEvents] tagged with the same ptyId.
  Future<int> openPty({
    required int sessionId,
    String command = '',
    int cols = 80,
    int rows = 24,
  }) async {
    final id = await _control.invokeMethod<int>('openPTY', {
      'sessionId': sessionId,
      'command': command,
      'cols': cols,
      'rows': rows,
    });
    if (id == null) throw StateError('bridge: openPTY returned null');
    return id;
  }

  /// Forwards a chunk of input bytes (keystrokes / paste payload) to the
  /// remote child process.
  Future<void> ptyInput({required int ptyId, required List<int> data}) async {
    await _control.invokeMethod<void>('ptyInput', {
      'ptyId': ptyId,
      'data': data is Uint8List ? data : Uint8List.fromList(data),
    });
  }

  /// Notifies the remote PTY of a terminal grid resize.
  Future<void> ptyResize({required int ptyId, required int cols, required int rows}) async {
    await _control.invokeMethod<void>('ptyResize', {
      'ptyId': ptyId,
      'cols': cols,
      'rows': rows,
    });
  }

  /// Closes a PTY. Idempotent.
  Future<void> closePty({required int ptyId}) async {
    await _control.invokeMethod<void>('closePTY', {
      'ptyId': ptyId,
    });
  }

  /// Broadcast stream of one-shot Exec events from all open sessions.
  /// PTY events on the same channel are filtered out.
  Stream<SessionEvent> events() => _eventsStream
      .where((raw) {
        final m = raw as Map<dynamic, dynamic>;
        final type = m['type'] as String?;
        return type == 'stdout' || type == 'stderr' || type == 'done';
      })
      .map((raw) => SessionEvent.fromMap(raw as Map<dynamic, dynamic>));

  /// Broadcast stream of PTY data + exit events tagged with ptyId.
  Stream<PtyEvent> ptyEvents() => _eventsStream
      .map((raw) => PtyEvent.fromMap(raw as Map<dynamic, dynamic>))
      .where((e) => e != null)
      .cast<PtyEvent>();
}
