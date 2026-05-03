import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// Per-server snapshot of the terminal tab strip (order, active index,
/// and per-tab reattach handle + label hint). Persisted via SecureStore
/// so the user can be killed by the OS and find every tab back exactly
/// where they left off — including the active selection — on next
/// launch, with no bottom-sheet picker in between.
class PersistedTabs {
  PersistedTabs({this.byServer = const <String, TabsSnapshot>{}});

  /// serverId -> ordered tab snapshot.
  final Map<String, TabsSnapshot> byServer;

  TabsSnapshot? forServer(String serverId) => byServer[serverId];

  Map<String, dynamic> toJson() => {
        'v': 1,
        'byServer': {
          for (final e in byServer.entries) e.key: e.value.toJson(),
        },
      };

  factory PersistedTabs.fromJson(Map<String, dynamic> j) {
    final raw = (j['byServer'] as Map?) ?? {};
    return PersistedTabs(
      byServer: {
        for (final entry in raw.entries)
          entry.key.toString(): TabsSnapshot.fromJson(
            (entry.value as Map).cast<String, dynamic>(),
          ),
      },
    );
  }
}

class TabsSnapshot {
  TabsSnapshot({this.activeIndex = 0, this.tabs = const <TabEntry>[]});

  final int activeIndex;
  final List<TabEntry> tabs;

  Map<String, dynamic> toJson() => {
        'activeIndex': activeIndex,
        'tabs': tabs.map((t) => t.toJson()).toList(),
      };

  factory TabsSnapshot.fromJson(Map<String, dynamic> j) {
    final raw = (j['tabs'] as List?) ?? const <dynamic>[];
    return TabsSnapshot(
      activeIndex: (j['activeIndex'] as num?)?.toInt() ?? 0,
      tabs: raw
          .map((e) => TabEntry.fromJson((e as Map).cast<String, dynamic>()))
          .toList(growable: false),
    );
  }
}

class TabEntry {
  TabEntry({required this.handle, this.label = '', this.lastCwd = ''});

  /// Reattach handle the host issued for this tab's PTY. Empty entries
  /// are filtered out by the persistence layer — a tab that never
  /// finished opening before the app died is not worth restoring.
  final String handle;
  final String label;
  final String lastCwd;

  Map<String, dynamic> toJson() =>
      {'h': handle, 'label': label, 'cwd': lastCwd};

  factory TabEntry.fromJson(Map<String, dynamic> j) => TabEntry(
        handle: (j['h'] as String?) ?? '',
        label: (j['label'] as String?) ?? '',
        lastCwd: (j['cwd'] as String?) ?? '',
      );
}

class PersistedTabsNotifier extends AsyncNotifier<PersistedTabs> {
  late final _store = ref.read(secureStoreProvider);
  static const _key = 'tabs.v1';

  @override
  Future<PersistedTabs> build() async {
    final raw = await _store.readKey(_key);
    if (raw == null || raw.isEmpty) return PersistedTabs();
    try {
      return PersistedTabs.fromJson(
          jsonDecode(raw) as Map<String, dynamic>);
    } catch (_) {
      return PersistedTabs();
    }
  }

  /// Replace the snapshot for [serverId] in one shot. Empty-handle
  /// entries are dropped so half-opened tabs don't survive a restart.
  Future<void> save({
    required String serverId,
    required TabsSnapshot snapshot,
  }) async {
    if (serverId.isEmpty) return;
    final filtered = snapshot.tabs
        .where((t) => t.handle.isNotEmpty)
        .toList(growable: false);
    final clamped = filtered.isEmpty
        ? 0
        : snapshot.activeIndex.clamp(0, filtered.length - 1);
    final next = PersistedTabs(byServer: {
      ...?state.valueOrNull?.byServer,
      serverId: TabsSnapshot(activeIndex: clamped, tabs: filtered),
    });
    await _store.writeKey(_key, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }

  /// Drop a single handle (e.g. its PTY no longer exists on the host).
  Future<void> forget({
    required String serverId,
    required String handle,
  }) async {
    final current = state.valueOrNull;
    if (current == null) return;
    final stored = current.forServer(serverId);
    if (stored == null) return;
    final remaining = stored.tabs
        .where((t) => t.handle != handle)
        .toList(growable: false);
    final byServer = {...current.byServer};
    if (remaining.isEmpty) {
      byServer.remove(serverId);
    } else {
      byServer[serverId] = TabsSnapshot(
        activeIndex: stored.activeIndex.clamp(0, remaining.length - 1),
        tabs: remaining,
      );
    }
    final next = PersistedTabs(byServer: byServer);
    await _store.writeKey(_key, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }

  Future<void> clear({required String serverId}) async {
    final current = state.valueOrNull;
    if (current == null || !current.byServer.containsKey(serverId)) return;
    final byServer = {...current.byServer}..remove(serverId);
    final next = PersistedTabs(byServer: byServer);
    await _store.writeKey(_key, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }
}

final persistedTabsProvider =
    AsyncNotifierProvider<PersistedTabsNotifier, PersistedTabs>(
  PersistedTabsNotifier.new,
);
