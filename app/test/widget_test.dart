// Phase 4b smoke test — confirms the basic widget tree compiles and
// renders without exceptions. Real verification of the gomobile bridge
// and the screens that depend on persistent state happens via the
// instrumented APK on a physical device, documented in
// docs/architecture.md.

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:app/screens/server_editor_screen.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  // flutter_secure_storage talks to a platform channel that does not
  // exist under flutter_test; mock it so any provider that touches it
  // returns empty quickly instead of hanging.
  setUpAll(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(
      const MethodChannel('plugins.it_nomads.com/flutter_secure_storage'),
      (call) async {
        if (call.method == 'read') return null;
        if (call.method == 'readAll') return <String, String>{};
        return null;
      },
    );
  });

  testWidgets('server editor renders required fields', (tester) async {
    await tester.pumpWidget(
      const ProviderScope(
        child: MaterialApp(home: ServerEditorScreen()),
      ),
    );
    await tester.pump();
    expect(find.text('Add server'), findsOneWidget);
    expect(find.text('Save'), findsOneWidget);
    expect(find.text('PSK (hex)'), findsOneWidget);
    expect(find.text('Target device_id'), findsOneWidget);
  });
}
