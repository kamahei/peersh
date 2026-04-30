// Phase 4a smoke test — confirms the app builds and the spike screen
// renders. Real device verification of the gomobile bridge is documented
// in docs/architecture.md and is performed by hand against a running
// peershd.

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:app/spike_screen.dart';

void main() {
  testWidgets('spike screen renders inputs and Run button', (tester) async {
    await tester.pumpWidget(
      const ProviderScope(
        child: MaterialApp(home: SpikeScreen()),
      ),
    );
    expect(find.text('peersh — gomobile spike'), findsOneWidget);
    expect(find.text('Run'), findsOneWidget);
    expect(find.byType(TextField), findsNWidgets(2));
  });
}
