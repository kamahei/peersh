// Phase 5b — device discovery against a backend store.
//
// Phase 4b shipped single-target-per-server: the user pasted the host's
// `device_id` (16 base32 chars from peershd's startup log) into the
// server entry and that's what the mobile app dialed. Useful for one
// host but tedious for several.
//
// Phase 5b lifts this restriction by reading a Firestore-backed
// device list. peersh-signaling writes
// `users/{uid}/devices/{deviceId}` on each Register frame (with kind,
// display_name, last_seen_at); the mobile app reads the same
// collection and surfaces a picker.

import 'package:cloud_firestore/cloud_firestore.dart';

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

/// Firestore-backed discovery for Firebase-mode servers. Reads
/// `users/{uid}/devices` and returns Windows-host entries sorted by
/// most-recently-seen. Caller must already be signed in (the rules
/// require auth.uid == userId).
class FirebaseDeviceDiscoveryService implements DeviceDiscoveryService {
  FirebaseDeviceDiscoveryService({required this.uid, FirebaseFirestore? db})
      : _db = db ?? FirebaseFirestore.instance;

  final String uid;
  final FirebaseFirestore _db;

  /// Server-side `core/proto/peersh/signal/v1.DeviceKind` enum:
  ///   0 = unspecified
  ///   1 = mobile client
  ///   2 = windows host
  static const int _kindWindowsHost = 2;

  @override
  Future<List<DiscoveredDevice>> list(ServerEntry server) async {
    final snap = await _db
        .collection('users')
        .doc(uid)
        .collection('devices')
        .get();
    final out = <DiscoveredDevice>[];
    for (final d in snap.docs) {
      final data = d.data();
      final kind = (data['kind'] as num?)?.toInt() ?? 0;
      if (kind != _kindWindowsHost) continue;
      out.add(DiscoveredDevice(
        deviceId: d.id,
        displayName: (data['display_name'] as String?)?.trim().isNotEmpty == true
            ? data['display_name'] as String
            : d.id,
        kind: 'host',
        lastSeenUnixMs: _readMillis(data['last_seen_at']),
      ));
    }
    out.sort((a, b) => b.lastSeenUnixMs.compareTo(a.lastSeenUnixMs));
    return out;
  }

  static int _readMillis(Object? v) {
    if (v is Timestamp) return v.millisecondsSinceEpoch;
    if (v is DateTime) return v.millisecondsSinceEpoch;
    if (v is int) return v;
    return 0;
  }
}
