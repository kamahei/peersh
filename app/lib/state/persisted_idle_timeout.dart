import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// User preference for how long the host should keep a detached
/// session + its persisted PTYs alive after the client disconnects.
/// Sent to peershd on ClientHello as idle_timeout_sec; the host clamps
/// it to its own [pwsh.MinIdleTimeout, pwsh.MaxIdleTimeout] window.
///
/// Stored in SecureStore alongside the rest of the app's settings, but
/// under its own key so it round-trips independently of the server list
/// or notification config.
class PersistedIdleTimeoutNotifier extends AsyncNotifier<int> {
  late final _store = ref.read(secureStoreProvider);
  static const _key = 'idle_timeout_sec.v1';

  /// 24 hours. Matches peershd's DefaultIdleTimeout so first-launch
  /// behavior is identical whether or not the user has touched the
  /// preference.
  static const defaultSeconds = 24 * 60 * 60;

  @override
  Future<int> build() async {
    final raw = await _store.readKey(_key);
    if (raw == null || raw.isEmpty) return defaultSeconds;
    final v = int.tryParse(raw);
    if (v == null || v <= 0) return defaultSeconds;
    return v;
  }

  Future<void> set(int seconds) async {
    final v = seconds <= 0 ? defaultSeconds : seconds;
    await _store.writeKey(_key, v.toString());
    state = AsyncData(v);
  }
}

final persistedIdleTimeoutProvider =
    AsyncNotifierProvider<PersistedIdleTimeoutNotifier, int>(
  PersistedIdleTimeoutNotifier.new,
);
