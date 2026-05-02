import 'dart:async';
import 'dart:convert';

import 'package:firebase_app_check/firebase_app_check.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../bridge.dart';
import '../models/server_entry.dart';
import '../models/session_event.dart';

/// One Riverpod-friendly active session. Owns a session id from the
/// bridge plus a buffered output stream the UI can consume.
class PeershSession {
  PeershSession._(this.bridge, this.id, this.serverId, this._eventsSub) {
    _eventsSub.onData(_onEvent);
  }

  /// Open a signaling-mediated session against [server] targeting
  /// [server.targetDeviceId].
  ///
  /// For Firebase server entries, [firebaseIdToken] must be supplied
  /// (typically obtained via FirebaseAuthService.resolve). PSK entries
  /// ignore it and use the server entry's PSK.
  static Future<PeershSession> open({
    required PeershBridge bridge,
    required ServerEntry server,
    String? firebaseIdToken,
  }) async {
    final int id;
    if (server.authMode == ServerAuthMode.firebase) {
      if (firebaseIdToken == null || firebaseIdToken.isEmpty) {
        throw StateError('Firebase server requires a fresh ID token; sign in first.');
      }
      String appCheckToken = '';
      try {
        appCheckToken = await FirebaseAppCheck.instance.getToken() ?? '';
      } catch (e) {
        // App Check may be inactivated (no project) or temporarily
        // unreachable; fall through with an empty token. The server
        // logs a warning when `app_check_required = false`, or rejects
        // the Register frame when enforced.
        debugPrint('peersh: App Check getToken failed: $e');
      }
      id = await bridge.openFirebaseSignalingSession(
        signaling: server.wsUrl,
        idToken: firebaseIdToken,
        appCheckToken: appCheckToken,
        targetDeviceId: server.targetDeviceId,
        stunServer: server.stunServer,
      );
    } else {
      id = await bridge.openSignalingSession(
        signaling: server.wsUrl,
        user: server.userId,
        pskHex: server.pskHex,
        targetDeviceId: server.targetDeviceId,
        stunServer: server.stunServer,
      );
    }
    final controller = StreamController<SessionEvent>.broadcast();
    final sub = bridge.events().listen(null);
    final session = PeershSession._(bridge, id, server.id, sub).._sink = controller;
    sub.onData((evt) {
      if (evt.sessionId == id) {
        controller.add(evt);
        session._onEvent(evt);
      }
    });
    return session;
  }

  /// Open a direct (Phase 1 / spike) session at [addr].
  static Future<PeershSession> openDirect({
    required PeershBridge bridge,
    required String addr,
  }) async {
    final id = await bridge.openDirectSession(addr: addr);
    final controller = StreamController<SessionEvent>.broadcast();
    final sub = bridge.events().listen(null);
    final session = PeershSession._(bridge, id, '', sub).._sink = controller;
    sub.onData((evt) {
      if (evt.sessionId == id) {
        controller.add(evt);
        session._onEvent(evt);
      }
    });
    return session;
  }

  final PeershBridge bridge;
  final int id;
  /// The ServerEntry.id this session is bound to. Used to scope per-
  /// server local state (e.g. persisted PTY reattach handles). Empty
  /// for direct (non-signaling) sessions.
  final String serverId;
  final StreamSubscription<SessionEvent> _eventsSub;
  late final StreamController<SessionEvent> _sink;

  /// Aggregated stdout text seen so far; rebuilt incrementally on each
  /// event. Useful for the simple log view.
  final List<String> _lines = <String>[];
  String _stdoutPartial = '';
  String _stderrPartial = '';
  String? _completionError;

  /// Stream of events for this session.
  Stream<SessionEvent> get events => _sink.stream;

  /// Snapshot of buffered output lines (no trailing partial).
  List<String> get bufferedLines => List.unmodifiable(_lines);

  /// Last-known partial stdout chunk (text after the most recent newline).
  String get currentPartial => _stdoutPartial;

  /// Non-null after the most recent Exec completed with an error.
  String? get completionError => _completionError;

  void _onEvent(SessionEvent event) {
    if (event is StdoutEvent) {
      _appendLines(_stdoutPartial, event.data, (rest) => _stdoutPartial = rest);
    } else if (event is StderrEvent) {
      _appendLines(_stderrPartial, event.data, (rest) => _stderrPartial = rest);
    } else if (event is DoneEvent) {
      _completionError = event.isError ? event.error : null;
      // Flush any trailing partial as a final line.
      if (_stdoutPartial.isNotEmpty) {
        _lines.add(_stdoutPartial);
        _stdoutPartial = '';
      }
      if (_stderrPartial.isNotEmpty) {
        _lines.add(_stderrPartial);
        _stderrPartial = '';
      }
    }
  }

  void _appendLines(String partial, List<int> data, void Function(String) setPartial) {
    final text = partial + utf8.decode(data, allowMalformed: true);
    final pieces = text.split('\n');
    final completeLines = pieces.sublist(0, pieces.length - 1);
    for (final line in completeLines) {
      _lines.add(line.replaceAll('\r', ''));
    }
    setPartial(pieces.last);
  }

  /// Run a command on this session.
  Future<void> exec(String command) async {
    _completionError = null;
    await bridge.exec(sessionId: id, command: command);
  }

  /// Read a remote file as UTF-8 text via Get-Content.
  Future<String> readFile(String path) =>
      bridge.readFile(sessionId: id, path: path);

  /// Close the session.
  Future<void> close() async {
    await _eventsSub.cancel();
    await _sink.close();
    await bridge.closeSession(sessionId: id);
  }
}

/// One bridge per app, lazily constructed.
final bridgeProvider = Provider<PeershBridge>((ref) => PeershBridge());
