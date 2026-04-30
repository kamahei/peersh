// peersh mobile app — Phase 4a entry point.
//
// Phase 4a ships only the verification spike screen. Phase 4b will
// introduce real screens (pairing, server list, device list, terminal)
// and route between them.

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'spike_screen.dart';

void main() {
  runApp(const ProviderScope(child: PeershApp()));
}

class PeershApp extends StatelessWidget {
  const PeershApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'peersh',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: Colors.indigo),
        useMaterial3: true,
      ),
      home: const SpikeScreen(),
    );
  }
}
