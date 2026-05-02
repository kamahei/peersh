// Phase 5b — device discovery against a backend store.
//
// Phase 4b shipped single-target-per-server: the user pasted the host's
// `device_id` (16 base32 chars from peershd's startup log) into the
// server entry and that's what the mobile app dialed. Useful for one
// host but tedious for several.
//
// Phase 5b lifts this restriction by reading a Realtime-Database-backed
// device list. peershd writes
// `users/{uid}/devices/{deviceId}/last_seen_at` from its wake-listener
// runtime; the mobile app reads the same subtree and surfaces a picker.
// Display name and kind ride alongside last_seen_at when the signaling
// server's Register handler propagates them (today the server still
// writes those to Firestore — we treat their absence in RTDB as a
// missing field and fall back to the deviceId as the display label).

import 'package:firebase_database/firebase_database.dart';

import '../models/server_entry.dart';
import 'rtdb.dart';

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

/// Realtime-Database-backed discovery for Firebase-mode servers. Reads
/// `users/{uid}/devices` and returns entries sorted by most-recently-seen.
/// Caller must already be signed in (RTDB rules require auth.uid == uid).
class FirebaseDeviceDiscoveryService implements DeviceDiscoveryService {
  FirebaseDeviceDiscoveryService({required this.uid, FirebaseDatabase? db})
      : _db = db ?? peershDatabase;

  final String uid;
  final FirebaseDatabase _db;

  @override
  Future<List<DiscoveredDevice>> list(ServerEntry server) async {
    final snap = await _db.ref('users/$uid/devices').get();
    if (!snap.exists) return const [];
    final raw = snap.value;
    if (raw is! Map) return const [];
    final out = <DiscoveredDevice>[];
    raw.forEach((key, value) {
      if (key is! String) return;
      String? displayName;
      int lastSeen = 0;
      if (value is Map) {
        final dn = value['display_name'];
        if (dn is String && dn.trim().isNotEmpty) displayName = dn;
        final ls = value['last_seen_at'];
        if (ls is int) lastSeen = ls;
      } else if (value is int) {
        // Host wrote only last_seen_at as a leaf int.
        lastSeen = value;
      }
      out.add(DiscoveredDevice(
        deviceId: key,
        displayName: displayName ?? key,
        kind: 'host',
        lastSeenUnixMs: lastSeen,
      ));
    });
    out.sort((a, b) => b.lastSeenUnixMs.compareTo(a.lastSeenUnixMs));
    return out;
  }
}
