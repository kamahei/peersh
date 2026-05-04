import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// Per-(server, reattach handle) bell settings persisted via SecureStore
/// so the v2-B notification toggle + threshold survive app restarts and
/// reconnects. Keyed by reattach handle because it's the only stable
/// per-PTY identifier the client sees across sessions.
class NotifyConfig {
  const NotifyConfig({
    required this.enabled,
    required this.thresholdSec,
    required this.idleSec,
  });

  final bool enabled;
  final int thresholdSec;
  final int idleSec;

  Map<String, dynamic> toJson() => {
        'enabled': enabled,
        'thresholdSec': thresholdSec,
        'idleSec': idleSec,
      };

  factory NotifyConfig.fromJson(Map<String, dynamic> j) => NotifyConfig(
        enabled: j['enabled'] as bool? ?? false,
        thresholdSec: (j['thresholdSec'] as num?)?.toInt() ?? 10,
        idleSec: (j['idleSec'] as num?)?.toInt() ?? 0,
      );
}

class PersistedNotifyConfigs {
  PersistedNotifyConfigs({this.byKey = const <String, NotifyConfig>{}});

  /// Key format: "$serverId|$handle".
  final Map<String, NotifyConfig> byKey;

  static String _key(String serverId, String handle) => '$serverId|$handle';

  NotifyConfig? lookup(String serverId, String handle) {
    if (serverId.isEmpty || handle.isEmpty) return null;
    return byKey[_key(serverId, handle)];
  }

  Map<String, dynamic> toJson() => {
        'v': 1,
        'byKey': {
          for (final entry in byKey.entries) entry.key: entry.value.toJson(),
        },
      };

  factory PersistedNotifyConfigs.fromJson(Map<String, dynamic> j) {
    final raw = (j['byKey'] as Map?) ?? const {};
    return PersistedNotifyConfigs(
      byKey: {
        for (final entry in raw.entries)
          entry.key.toString(): NotifyConfig.fromJson(
              (entry.value as Map).cast<String, dynamic>()),
      },
    );
  }
}

class PersistedNotifyConfigsNotifier
    extends AsyncNotifier<PersistedNotifyConfigs> {
  late final _store = ref.read(secureStoreProvider);
  static const _storageKey = 'notify_config.v1';

  @override
  Future<PersistedNotifyConfigs> build() async {
    final raw = await _store.readKey(_storageKey);
    if (raw == null || raw.isEmpty) return PersistedNotifyConfigs();
    try {
      return PersistedNotifyConfigs.fromJson(
          jsonDecode(raw) as Map<String, dynamic>);
    } catch (_) {
      return PersistedNotifyConfigs();
    }
  }

  Future<void> upsert({
    required String serverId,
    required String handle,
    required NotifyConfig config,
  }) async {
    if (serverId.isEmpty || handle.isEmpty) return;
    final current = state.value ?? PersistedNotifyConfigs();
    final next = PersistedNotifyConfigs(
      byKey: {
        ...current.byKey,
        PersistedNotifyConfigs._key(serverId, handle): config,
      },
    );
    await _store.writeKey(_storageKey, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }

  Future<void> forget({
    required String serverId,
    required String handle,
  }) async {
    final current = state.value;
    if (current == null) return;
    final key = PersistedNotifyConfigs._key(serverId, handle);
    if (!current.byKey.containsKey(key)) return;
    final next = PersistedNotifyConfigs(
      byKey: {...current.byKey}..remove(key),
    );
    await _store.writeKey(_storageKey, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }
}

final persistedNotifyConfigsProvider = AsyncNotifierProvider<
    PersistedNotifyConfigsNotifier, PersistedNotifyConfigs>(
  PersistedNotifyConfigsNotifier.new,
);
