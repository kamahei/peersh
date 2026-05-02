// MethodChannel + EventChannel bridge to mobile-core.
//
// Phase 4a shipped Version + Echo. Phase 4b adds the Session lifecycle:
// open (direct or signaling-mediated), exec (streaming output via the
// EventChannel), readFile (one-shot), close.

import 'package:flutter/services.dart';

import 'models/pty_event.dart';
import 'models/pty_file.dart';
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

  /// Phase 5b: opens a Firebase-authenticated signaling session. The
  /// caller supplies a fresh Firebase ID token; the server resolves
  /// user_id from it. [appCheckToken] is forwarded to the server's
  /// App Check verifier; pass empty when App Check is not in use.
  Future<int> openFirebaseSignalingSession({
    required String signaling,
    required String idToken,
    required String targetDeviceId,
    String appCheckToken = '',
    String stunServer = 'stun.l.google.com:19302',
  }) async {
    final id = await _control.invokeMethod<int>('openFirebaseSignalingSession', {
      'signaling': signaling,
      'idToken': idToken,
      'appCheckToken': appCheckToken,
      'target': targetDeviceId,
      'stun': stunServer,
    });
    if (id == null) throw StateError('bridge: openFirebaseSignalingSession returned null');
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

  /// Starts (or refreshes) the Android foreground service that holds
  /// the OS off the app process while a session is active. iOS has no
  /// equivalent yet (Background Modes work is deferred); the call is
  /// silently ignored on platforms without the handler.
  Future<void> startForegroundService({
    required String title,
    required String body,
  }) async {
    try {
      await _control.invokeMethod<void>('fgServiceStart', {
        'title': title,
        'body': body,
      });
    } catch (_) {
      // No-op on iOS / web / desktop.
    }
  }

  /// Stops the foreground service started via [startForegroundService].
  Future<void> stopForegroundService() async {
    try {
      await _control.invokeMethod<void>('fgServiceStop');
    } catch (_) {}
  }

  /// Returns true when system notifications are currently enabled for
  /// this app. False on Android 13+ when POST_NOTIFICATIONS hasn't
  /// been granted; the foreground service still runs but its
  /// notification is hidden. On non-Android platforms returns true.
  Future<bool> notificationsEnabled() async {
    try {
      final v = await _control.invokeMethod<bool>('notificationsEnabled');
      return v ?? true;
    } catch (_) {
      return true;
    }
  }

  /// Triggers the system POST_NOTIFICATIONS prompt on Android 13+.
  /// No-op on platforms / OS versions where the runtime permission
  /// isn't required.
  Future<void> requestNotifications() async {
    try {
      await _control.invokeMethod<void>('requestNotifications');
    } catch (_) {}
  }

  /// Opens the system Settings page for this app's notification
  /// settings (or app details when notification settings aren't
  /// reachable directly).
  Future<void> openNotificationSettings() async {
    try {
      await _control.invokeMethod<void>('openNotificationSettings');
    } catch (_) {}
  }

  /// Opens an interactive PTY on an existing session. Returns the host-
  /// assigned PTY id + the server-issued reattach handle. The caller
  /// uses the id for [ptyInput] / [ptyResize] / [closePty] / file API
  /// calls; the handle is what survives connection drops.
  ///
  /// Pass [reattachHandle] to bind to a previously-persisted PTY (the
  /// server replays its scrollback ring buffer first).
  Future<({int ptyId, String handle})> openPty({
    required int sessionId,
    String command = '',
    int cols = 80,
    int rows = 24,
    String reattachHandle = '',
  }) async {
    final raw = await _control.invokeMethod<Map<dynamic, dynamic>>('openPTY', {
      'sessionId': sessionId,
      'command': command,
      'cols': cols,
      'rows': rows,
      'reattachHandle': reattachHandle,
    });
    if (raw == null) throw StateError('bridge: openPTY returned null');
    return (
      ptyId: (raw['ptyId'] as num).toInt(),
      handle: (raw['handle'] as String?) ?? '',
    );
  }

  /// Returns the persisted PTYs the host is holding for this session.
  Future<List<PtyHandleInfo>> listPtys({required int sessionId}) async {
    final raw = await _control.invokeMethod<List<dynamic>>('listPTYs', {
      'sessionId': sessionId,
    });
    if (raw == null) return const [];
    return raw
        .cast<Map<dynamic, dynamic>>()
        .map(PtyHandleInfo.fromMap)
        .toList(growable: false);
  }

  /// Tear down a persisted PTY by its handle.
  Future<String> killPty({required int sessionId, required String handle}) async {
    final out = await _control.invokeMethod<String>('killPTY', {
      'sessionId': sessionId,
      'handle': handle,
    });
    return out ?? '';
  }

  /// Returns the host's last-observed cwd for the PTY (via OSC 9;9
  /// emitted by the prompt wrapper). Empty if the prompt has not
  /// rendered yet.
  Future<String> getCwd({required int ptyId}) async {
    final out = await _control.invokeMethod<String>('getCwd', {
      'ptyId': ptyId,
    });
    return out ?? '';
  }

  /// Lists files at a cwd-relative `path` inside the PTY's directory.
  /// Returns an empty list on failure (errors are swallowed and surfaced
  /// via the [PtyFileEntry.path] convention — callers wanting hard
  /// errors should use the platform error channel instead).
  Future<List<PtyFileEntry>> listSessionFiles({
    required int ptyId,
    required String path,
  }) async {
    final raw = await _control.invokeMethod<List<dynamic>>('listSessionFiles', {
      'ptyId': ptyId,
      'path': path,
    });
    if (raw == null) return const [];
    return raw
        .cast<Map<dynamic, dynamic>>()
        .map(PtyFileEntry.fromMap)
        .toList(growable: false);
  }

  /// Reads a cwd-relative file. Returns the content + metadata.
  Future<PtyFileContent> readSessionFile({
    required int ptyId,
    required String path,
    int maxBytes = 0,
  }) async {
    final raw = await _control.invokeMethod<Map<dynamic, dynamic>>(
      'readSessionFile',
      {
        'ptyId': ptyId,
        'path': path,
        'maxBytes': maxBytes,
      },
    );
    if (raw == null) {
      return const PtyFileContent(
          content: <int>[], encoding: '', size: 0, truncated: false, error: 'no response');
    }
    return PtyFileContent.fromMap(raw);
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
