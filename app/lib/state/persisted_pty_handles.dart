import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// Per-server set of reattach handles the client has seen the host
/// hand out. Persisted via SecureStore so the user can leave the app,
/// come back later, and rebind to the same shells.
class PersistedPtyHandles {
  PersistedPtyHandles({this.byServer = const <String, List<String>>{}});

  /// serverId -> ordered list of handles (most recent first).
  final Map<String, List<String>> byServer;

  List<String> forServer(String serverId) =>
      byServer[serverId] ?? const <String>[];

  Map<String, dynamic> toJson() => {'v': 1, 'byServer': byServer};

  factory PersistedPtyHandles.fromJson(Map<String, dynamic> j) {
    final raw = (j['byServer'] as Map?) ?? {};
    return PersistedPtyHandles(
      byServer: {
        for (final entry in raw.entries)
          entry.key.toString(): List<String>.from(
            (entry.value as List? ?? const <dynamic>[]).cast<String>(),
          ),
      },
    );
  }
}

/// Notifier that persists handle changes via secure_store. Stored under
/// a separate key from servers/settings so import/export stays clean.
class PersistedPtyHandlesNotifier extends AsyncNotifier<PersistedPtyHandles> {
  late final _store = ref.read(secureStoreProvider);
  static const _key = 'pty_handles.v1';

  @override
  Future<PersistedPtyHandles> build() async {
    final raw = await _store.readKey(_key);
    if (raw == null || raw.isEmpty) return PersistedPtyHandles();
    try {
      return PersistedPtyHandles.fromJson(
          jsonDecode(raw) as Map<String, dynamic>);
    } catch (_) {
      return PersistedPtyHandles();
    }
  }

  Future<void> remember(
      {required String serverId, required String handle}) async {
    if (serverId.isEmpty || handle.isEmpty) return;
    final current = state.value ?? PersistedPtyHandles();
    final list = List<String>.from(current.forServer(serverId))
      ..remove(handle)
      ..insert(0, handle);
    final next = PersistedPtyHandles(
      byServer: {...current.byServer, serverId: list},
    );
    await _store.writeKey(_key, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }

  Future<void> forget(
      {required String serverId, required String handle}) async {
    final current = state.value;
    if (current == null) return;
    final list = List<String>.from(current.forServer(serverId))..remove(handle);
    final next = PersistedPtyHandles(
      byServer: {...current.byServer, serverId: list},
    );
    await _store.writeKey(_key, jsonEncode(next.toJson()));
    state = AsyncData(next);
  }
}

final persistedPtyHandlesProvider = AsyncNotifierProvider<
    PersistedPtyHandlesNotifier, PersistedPtyHandles>(
  PersistedPtyHandlesNotifier.new,
);
