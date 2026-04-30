// MethodChannel bridge to mobile-core.
//
// Phase 4a is the verification spike: a single synchronous round-trip
// (one MethodChannel call → one QUIC session). Phase 4b will introduce an
// EventChannel for streaming output chunk-by-chunk.

import 'package:flutter/services.dart';

class PeershBridge {
  static const _channel = MethodChannel('dev.peersh/bridge');

  /// Returns mobile-core's build identifier — smoke test for "is the
  /// gomobile bind alive at all".
  Future<String> version() async {
    final v = await _channel.invokeMethod<String>('version');
    return v ?? '';
  }

  /// Dials [addr] (host:port) over QUIC, runs the protocol Hello, sends
  /// one ExecRequest with [command], drains stdout, and returns the
  /// concatenated stdout. On failure the returned string starts with
  /// "ERROR: " (the Go side packs errors into the success channel for
  /// gomobile-friendliness).
  Future<String> echo({required String addr, required String command}) async {
    final out = await _channel.invokeMethod<String>('echo', {
      'addr': addr,
      'command': command,
    });
    return out ?? '';
  }
}
