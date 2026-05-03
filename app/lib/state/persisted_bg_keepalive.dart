import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// Opt-in for keeping the QUIC connection alive while the app is in
/// the background. When false (default) we skip starting the Android
/// foreground service, letting the OS reclaim the UDP socket after a
/// few minutes — battery-friendly, and FCM + auto-reattach + the host
/// ring buffer cover most "left it backgrounded for a while" cases.
///
/// Turn on if you need real-time streaming output while the app is
/// off-screen (e.g. live tail of a long-running build). Without this,
/// only the last ~1 MiB / tab of scrollback is replayed on resume.
class PersistedBgKeepaliveNotifier extends AsyncNotifier<bool> {
  late final _store = ref.read(secureStoreProvider);
  static const _key = 'bg_keepalive.v1';

  static const defaultValue = false;

  @override
  Future<bool> build() async {
    final raw = await _store.readKey(_key);
    if (raw == null || raw.isEmpty) return defaultValue;
    return raw == '1' || raw.toLowerCase() == 'true';
  }

  Future<void> set(bool enabled) async {
    await _store.writeKey(_key, enabled ? '1' : '0');
    state = AsyncData(enabled);
  }
}

final persistedBgKeepaliveProvider =
    AsyncNotifierProvider<PersistedBgKeepaliveNotifier, bool>(
  PersistedBgKeepaliveNotifier.new,
);
