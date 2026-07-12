// End-to-end bridge test: drives the native mobile-core bridge (Dart ->
// MethodChannel -> Swift/Kotlin -> gomobile -> QUIC) against a peershd host
// running in DIRECT mode on the same machine, and asserts a shell command
// round-trips through a real PTY.
//
// This is a MANUAL/local harness — it needs a peershd listening on
// 127.0.0.1:7777, so it is never run by plain `flutter test` (which skips
// integration_test/). Run it explicitly against a booted device/simulator:
//
//   # host side (repo root):
//   go build -o /tmp/peershd ./windows/cmd/peershd
//   /tmp/peershd -listen 127.0.0.1:7777
//   # app side:
//   cd app && flutter test integration_test/bridge_e2e_test.dart -d <device-id>
//
// On the iOS Simulator, 127.0.0.1 reaches the host Mac's loopback, so the same
// address works from the simulated app. It proves the iOS/Android bridge's full
// session + PTY path, not just version().
import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';

import 'package:app/bridge.dart';
import 'package:app/models/pty_event.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  const addr = String.fromEnvironment('PEERSH_ADDR', defaultValue: '127.0.0.1:7777');
  const marker = 'PEERSH_BRIDGE_E2E_OK';

  testWidgets('direct-session PTY round-trip through the native bridge',
      (tester) async {
    final bridge = PeershBridge();

    // 1) Bridge is live (Dart -> native -> gomobile).
    final version = await bridge.version();
    expect(version, isNotEmpty, reason: 'bridge.version() should return the mobile-core build');

    // 2) Open a direct QUIC session to peershd and a PTY on it.
    final sessionId = await bridge.openDirectSession(addr: addr);
    final pty = await bridge.openPty(sessionId: sessionId, cols: 80, rows: 24);

    // 3) Collect PTY output; complete once the echoed marker shows up.
    final out = StringBuffer();
    final sawMarker = Completer<void>();
    final sub = bridge.ptyEvents().listen((e) {
      if (e is PtyDataEvent && e.ptyId == pty.ptyId) {
        out.write(utf8.decode(e.data, allowMalformed: true));
        if (out.toString().contains(marker) && !sawMarker.isCompleted) {
          sawMarker.complete();
        }
      }
    });

    // 4) Let the shell prompt render, then run a command whose output is unique.
    await Future<void>.delayed(const Duration(seconds: 2));
    await bridge.ptyInput(ptyId: pty.ptyId, data: utf8.encode('echo $marker\n'));

    await sawMarker.future.timeout(
      const Duration(seconds: 12),
      onTimeout: () =>
          fail('marker "$marker" not seen in PTY output:\n${out.toString()}'),
    );

    await sub.cancel();
    await bridge.closePty(ptyId: pty.ptyId);
    await bridge.closeSession(sessionId: sessionId);

    expect(out.toString(), contains(marker));
  });
}
