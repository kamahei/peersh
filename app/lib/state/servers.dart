import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/server_entry.dart';
import 'secure_store.dart';

/// Single SecureStore instance shared by all providers.
final secureStoreProvider = Provider<SecureStore>((ref) => SecureStore());

/// AsyncNotifier holding the persisted server list.
class ServersNotifier extends AsyncNotifier<List<ServerEntry>> {
  late final SecureStore _store = ref.read(secureStoreProvider);

  @override
  Future<List<ServerEntry>> build() async => _store.readServers();

  Future<void> add(ServerEntry entry) async {
    final next = <ServerEntry>[...?state.value, entry];
    await _store.writeServers(next);
    state = AsyncData(next);
  }

  Future<void> replace(ServerEntry entry) async {
    final current = state.value ?? const <ServerEntry>[];
    final next = [
      for (final e in current)
        if (e.id == entry.id) entry else e,
    ];
    await _store.writeServers(next);
    state = AsyncData(next);
  }

  Future<void> remove(String id) async {
    final current = state.value ?? const <ServerEntry>[];
    final next = current.where((e) => e.id != id).toList();
    await _store.writeServers(next);
    state = AsyncData(next);
  }
}

final serversProvider =
    AsyncNotifierProvider<ServersNotifier, List<ServerEntry>>(
        ServersNotifier.new);
