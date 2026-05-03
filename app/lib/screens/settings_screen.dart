import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../services/flavor.dart' as flavor;
import '../spike_screen.dart';
import '../state/persisted_bg_keepalive.dart';
import '../state/persisted_idle_timeout.dart';
import '../state/settings.dart';
import 'pair_pc_screen.dart';

/// Choices the user picks between for "Keep shells alive for". Aligned
/// with peershd's pwsh.MinIdleTimeout (1 min) and MaxIdleTimeout (7 d)
/// bounds — values outside that range get clamped server-side anyway.
const _idleTimeoutChoices = <(String, int)>[
  ('30 minutes', 30 * 60),
  ('2 hours', 2 * 60 * 60),
  ('24 hours', 24 * 60 * 60),
  ('1 week', 7 * 24 * 60 * 60),
];

String _idleTimeoutLabel(int sec) {
  for (final (label, value) in _idleTimeoutChoices) {
    if (value == sec) return label;
  }
  if (sec >= 24 * 60 * 60) return '${sec ~/ (24 * 60 * 60)} d';
  if (sec >= 60 * 60) return '${sec ~/ (60 * 60)} h';
  return '${sec ~/ 60} min';
}

class _BgKeepaliveTile extends ConsumerWidget {
  const _BgKeepaliveTile();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(persistedBgKeepaliveProvider);
    final on = async.valueOrNull ?? PersistedBgKeepaliveNotifier.defaultValue;
    return SwitchListTile(
      title: const Text('Keep connection alive in background'),
      subtitle: const Text('Applies on next connect.'),
      value: on,
      onChanged: (v) =>
          ref.read(persistedBgKeepaliveProvider.notifier).set(v),
    );
  }
}

class _IdleTimeoutTile extends ConsumerWidget {
  const _IdleTimeoutTile();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(persistedIdleTimeoutProvider);
    final current = async.valueOrNull ??
        PersistedIdleTimeoutNotifier.defaultSeconds;
    return ListTile(
      title: const Text('Keep shells alive for'),
      subtitle: Text(_idleTimeoutLabel(current)),
      trailing: PopupMenuButton<int>(
        initialValue: current,
        onSelected: (sec) =>
            ref.read(persistedIdleTimeoutProvider.notifier).set(sec),
        itemBuilder: (_) => [
          for (final (label, value) in _idleTimeoutChoices)
            PopupMenuItem(value: value, child: Text(label)),
        ],
        child: const Icon(Icons.more_horiz),
      ),
    );
  }
}

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
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 8, 16, 0),
              child: Text(
                'Notification defaults',
                style: TextStyle(fontWeight: FontWeight.bold),
              ),
            ),
            ListTile(
              title: const Text('Default command-finished threshold'),
              subtitle: Slider(
                value: s.defaultNotifyThresholdSec.toDouble(),
                min: 1,
                max: 120,
                divisions: 119,
                label: '${s.defaultNotifyThresholdSec}s',
                onChanged: (v) => ref
                    .read(settingsProvider.notifier)
                    .setDefaultNotifyThresholdSec(v.round()),
              ),
              trailing: Text('${s.defaultNotifyThresholdSec}s'),
            ),
            ListTile(
              title: const Text('Default idle-silence window'),
              subtitle: Slider(
                value: s.defaultNotifyIdleSec.toDouble(),
                min: 0,
                max: 60,
                divisions: 60,
                label: s.defaultNotifyIdleSec == 0
                    ? 'off'
                    : '${s.defaultNotifyIdleSec}s',
                onChanged: (v) => ref
                    .read(settingsProvider.notifier)
                    .setDefaultNotifyIdleSec(v.round()),
              ),
              trailing: Text(s.defaultNotifyIdleSec == 0
                  ? 'off'
                  : '${s.defaultNotifyIdleSec}s'),
            ),
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: Text(
                'Long-press the bell on a tab to override these per tab.',
                style: TextStyle(fontSize: 12, color: Colors.grey),
              ),
            ),
            const Divider(),
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 8, 16, 0),
              child: Text(
                'Shell lifetime',
                style: TextStyle(fontWeight: FontWeight.bold),
              ),
            ),
            const _IdleTimeoutTile(),
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: Text(
                'Applies on next connect. Longer windows let the app survive being killed by the OS without losing your shells.',
                style: TextStyle(fontSize: 12, color: Colors.grey),
              ),
            ),
            const Divider(),
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 8, 16, 0),
              child: Text(
                'Background behavior',
                style: TextStyle(fontWeight: FontWeight.bold),
              ),
            ),
            const _BgKeepaliveTile(),
            const Padding(
              padding: EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: Text(
                'Off (default): saves battery by letting the OS reclaim the connection when backgrounded. '
                'Output streamed while you were away is replayed (up to ~1 MiB / tab) when you return.\n'
                'On: keeps the QUIC connection alive via a persistent notification so you see real-time output even from the background.',
                style: TextStyle(fontSize: 12, color: Colors.grey),
              ),
            ),
            const Divider(),
            if (flavor.kFirebaseInitialized)
              ListTile(
                leading: const Icon(Icons.dialpad_outlined),
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
