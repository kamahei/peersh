// MobileDeviceRegistry — keeps users/{uid}/devices/{mobileDeviceId}
// in RTDB up to date with the FCM token + last-seen for this phone.
//
// Why a separate id from the mobile-core mTLS device_id? The mTLS
// device_id rotates if the keystore is wiped (which happens on app
// data clear); the FCM token is also reset in that case, but we want
// the RTDB device record to be a stable identifier the user can see
// in the picker even if the underlying mTLS material has been
// regenerated. So this is a UUID-shaped opaque id stored once in
// flutter_secure_storage on first launch.
//
// On each sign-in (and on every onTokenRefresh), the registry writes:
//   users/{uid}/devices/{mobileDeviceId}/
//     fcm_token:    <current token>
//     kind:         "mobile"
//     display_name: <e.g. "Pixel 8 Pro">
//     last_seen_at: ServerValue.timestamp

import 'dart:async';
import 'dart:io' show Platform;
import 'dart:math';

import 'package:firebase_database/firebase_database.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import 'fcm_service.dart';
import 'rtdb.dart';

/// Storage key for the per-install mobileDeviceId. Exposed so other
/// services (e.g. the tab notification toggle) can read the same id
/// without instantiating a full registry.
const mobileDeviceIdStorageKey = 'peersh.mobile_device_id';

/// Returns the stable per-install mobileDeviceId, generating one on
/// first call. Same lookup the registry uses internally.
Future<String> readOrCreateMobileDeviceId({
  FlutterSecureStorage? storage,
}) async {
  final s = storage ?? const FlutterSecureStorage();
  final existing = await s.read(key: mobileDeviceIdStorageKey);
  if (existing != null && existing.isNotEmpty) return existing;
  final r = Random.secure();
  final buf = StringBuffer();
  for (var i = 0; i < 16; i++) {
    buf.write(r.nextInt(256).toRadixString(16).padLeft(2, '0'));
  }
  final fresh = buf.toString();
  await s.write(key: mobileDeviceIdStorageKey, value: fresh);
  return fresh;
}

class MobileDeviceRegistry {
  MobileDeviceRegistry({
    required FcmService fcm,
    FirebaseDatabase? db,
    FlutterSecureStorage? storage,
    String? displayNameOverride,
  })  : _fcm = fcm,
        _db = db ?? peershDatabase,
        _storage = storage ?? const FlutterSecureStorage(),
        _displayNameOverride = displayNameOverride;

  final FcmService _fcm;
  final FirebaseDatabase _db;
  final FlutterSecureStorage _storage;
  final String? _displayNameOverride;

  String? _cachedDeviceId;
  StreamSubscription<String>? _refreshSub;

  /// Returns the stable per-install mobileDeviceId, generating one
  /// on first call. Delegates to readOrCreateMobileDeviceId so other
  /// services (e.g. the tab notification toggle) can use the same id
  /// without instantiating a full registry.
  Future<String> mobileDeviceId() async {
    if (_cachedDeviceId != null) return _cachedDeviceId!;
    final id = await readOrCreateMobileDeviceId(storage: _storage);
    _cachedDeviceId = id;
    return id;
  }

  /// Writes the current FCM token + metadata under the given uid.
  /// Idempotent; safe to call on every sign-in event.
  Future<void> register(String uid) async {
    if (uid.isEmpty) return;
    try {
      final tok = await _fcm.token();
      if (tok.isEmpty) {
        debugPrint('peersh: FCM token unavailable; skip device register');
        return;
      }
      final id = await mobileDeviceId();
      await _db.ref('users/$uid/devices/$id').update({
        'fcm_token': tok,
        'kind': 'mobile',
        'display_name': _displayName(),
        'last_seen_at': ServerValue.timestamp,
      });
      debugPrint('peersh: registered mobile device $id for uid $uid');

      // Subscribe (idempotent) to token refreshes so the registered
      // value never goes stale.
      _refreshSub ??= _fcm.onTokenRefresh.listen((newToken) async {
        try {
          await _db.ref('users/$uid/devices/$id').update({
            'fcm_token': newToken,
            'last_seen_at': ServerValue.timestamp,
          });
          debugPrint('peersh: refreshed FCM token for uid $uid');
        } catch (e) {
          debugPrint('peersh: token refresh write failed: $e');
        }
      });
    } catch (e) {
      debugPrint('peersh: device registration failed: $e');
    }
  }

  /// Cancel the refresh subscription (used at sign-out).
  Future<void> dispose() async {
    await _refreshSub?.cancel();
    _refreshSub = null;
  }

  String _displayName() {
    if (_displayNameOverride != null && _displayNameOverride.isNotEmpty) {
      return _displayNameOverride;
    }
    if (Platform.isAndroid) return 'Android';
    if (Platform.isIOS) return 'iOS';
    return 'mobile';
  }

}
