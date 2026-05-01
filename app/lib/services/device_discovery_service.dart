// Phase 5b scaffolding — device discovery against a backend store.
//
// Phase 4b ships single-target-per-server: the user pastes the host's
// `device_id` (16 base32 chars from peershd's startup log) into the
// server entry and that's what the mobile app dials. It works for
// personal use but doesn't scale — every host you own needs a fresh
// server entry.
//
// Phase 5b lifts this restriction by reading a Firestore-backed
// device list. The host (peershd) registers its own device document
// under `users/{uid}/devices/{deviceId}` on each connect; the mobile
// app reads the same collection and surfaces a picker.
//
// Today this file only ships the PSK fallback (a single hard-coded
// device matching whatever the user typed in the server editor).
// FirebaseDeviceDiscoveryService will replace it once the operator
// configures Firebase.

import '../models/server_entry.dart';

/// One device the mobile app may dial.
class DiscoveredDevice {
  const DiscoveredDevice({
    required this.deviceId,
    required this.displayName,
    this.kind = 'host',
    this.lastSeenUnixMs = 0,
  });

  /// 16-char base32 device id derived from the host's public key.
  final String deviceId;

  /// Human-readable name (e.g. host name). Defaults to the deviceId
  /// when no nicer name is known.
  final String displayName;

  /// "host" | "mobile_client" | "cli". Today only host devices show
  /// up in the picker.
  final String kind;

  /// Wall-clock time of the host's most recent register against the
  /// signaling server. 0 when unknown.
  final int lastSeenUnixMs;
}

/// Pluggable device-discovery backend.
abstract class DeviceDiscoveryService {
  Future<List<DiscoveredDevice>> list(ServerEntry server);
}

/// PSK fallback — returns whatever the user typed into ServerEntry.
/// Phase 4b's behaviour, lifted into this interface so the rest of
/// the app can consume the abstraction.
class PskDeviceDiscoveryService implements DeviceDiscoveryService {
  const PskDeviceDiscoveryService();

  @override
  Future<List<DiscoveredDevice>> list(ServerEntry server) async {
    if (server.targetDeviceId.isEmpty) return const [];
    return [
      DiscoveredDevice(
        deviceId: server.targetDeviceId,
        displayName: server.targetDeviceId,
      ),
    ];
  }
}
