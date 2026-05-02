import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../services/flavor.dart' as flavor;
import '../spike_screen.dart';
import '../state/settings.dart';
import 'pair_pc_screen.dart';

class SettingsScreen extends ConsumerWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final settingsAsync = ref.watch(settingsProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('Settings')),
      body: settingsAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: SelectableText('$e')),
        data: (s) => ListView(
          children: [
            SwitchListTile(
              title: const Text('Default to wrap mode'),
              subtitle: const Text(
                  'Wrap long lines at the viewport edge instead of horizontal scroll.'),
              value: s.lineWrap,
              onChanged: (v) =>
                  ref.read(settingsProvider.notifier).setLineWrap(v),
            ),
            ListTile(
              title: const Text('Terminal font size'),
              subtitle: Slider(
                value: s.fontSize,
                min: 10,
                max: 22,
                divisions: 12,
                label: s.fontSize.toStringAsFixed(0),
                onChanged: (v) =>
                    ref.read(settingsProvider.notifier).setFontSize(v),
              ),
              trailing: Text(s.fontSize.toStringAsFixed(0)),
            ),
            const Divider(),
            if (flavor.kFirebaseInitialized)
              ListTile(
                leading: const Icon(Icons.qr_code_2_outlined),
                title: const Text('Pair PC'),
                subtitle: const Text(
                    'Generate a one-time code so peershd can mint Firebase tokens for your account.'),
                onTap: () => Navigator.of(context).push(
                  MaterialPageRoute(builder: (_) => const PairPcScreen()),
                ),
              ),
            ListTile(
              leading: const Icon(Icons.bug_report_outlined),
              title: const Text('Developer spike screen'),
              subtitle:
                  const Text('Direct QUIC dial without signaling (Phase 4a).'),
              onTap: () => Navigator.of(context).push(
                MaterialPageRoute(builder: (_) => const SpikeScreen()),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
