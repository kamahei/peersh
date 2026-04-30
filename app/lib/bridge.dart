// MethodChannel + EventChannel bridge to mobile-core.
//
// Phase 4a shipped Version + Echo. Phase 4b adds the Session lifecycle:
// open (direct or signaling-mediated), exec (streaming output via the
// EventChannel), readFile (one-shot), close.

import 'package:flutter/services.dart';

import 'models/session_event.dart';

class PeershBridge {
  static const _control = MethodChannel('dev.peersh/bridge');
  static const _events = EventChannel('dev.peersh/session/events');

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

  /// Broadcast stream of events from all open sessions, tagged with
  /// sessionId.
  Stream<SessionEvent> events() => _events
      .receiveBroadcastStream()
      .map((raw) => SessionEvent.fromMap(raw as Map<dynamic, dynamic>));
}
