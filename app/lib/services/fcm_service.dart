// Firebase Cloud Messaging integration for the v2-B push-notification
// path.
//
// In Firebase mode the mobile app receives a notification per
// command-completion event the host detects (OSC 9;9 or idle
// silence). FirebaseFcmService is the abstraction surface between the
// Firebase SDK and the rest of the app:
//
//   - permissions (POST_NOTIFICATIONS on Android 13+; iOS prompt on
//     first registration);
//   - the current FCM device token, refreshed via the SDK's
//     onTokenRefresh stream;
//   - foreground / background / cold-start message streams that
//     the deep-link router consumes.
//
// MobileDeviceRegistry (in mobile_device_registry.dart) writes the
// registered token + a stable mobileDeviceId into RTDB so the host's
// notification flow can target this phone.

import 'dart:async';
import 'dart:io' show Platform;

import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

abstract class FcmService {
  /// Returns true when the OS / user has granted notification
  /// permission AND a Firebase project is configured.
  Future<bool> isReady();

  /// Current FCM device token, or empty when not yet registered.
  Future<String> token();

  /// Stream of foreground messages received while the app is in the
  /// foreground. The default consumer is silent — the OS already
  /// shows backgrounded notifications, and overlaying one on top of
  /// the active app is noisy.
  Stream<RemoteMessage> get onMessage;

  /// Stream of notification taps that opened the app from a
  /// background state.
  Stream<RemoteMessage> get onMessageOpenedApp;

  /// Cold-start tap: returns the message that launched the app, or
  /// null if the user didn't tap a notification.
  Future<RemoteMessage?> getInitialMessage();

  /// Stream of token refreshes; consumers should re-register to RTDB.
  Stream<String> get onTokenRefresh;
}

class NoopFcmService implements FcmService {
  const NoopFcmService();

  @override
  Future<bool> isReady() async => false;

  @override
  Future<String> token() async => '';

  @override
  Stream<RemoteMessage> get onMessage => const Stream.empty();

  @override
  Stream<RemoteMessage> get onMessageOpenedApp => const Stream.empty();

  @override
  Future<RemoteMessage?> getInitialMessage() async => null;

  @override
  Stream<String> get onTokenRefresh => const Stream.empty();
}

/// FirebaseFcmService wires the firebase_messaging SDK into the rest
/// of the app. Construction is cheap; `ensurePermission` and `token`
/// are the entry points.
class FirebaseFcmService implements FcmService {
  FirebaseFcmService({FirebaseMessaging? fm, MethodChannel? bridge})
      : _fm = fm ?? FirebaseMessaging.instance,
        _bridge = bridge ?? const MethodChannel('dev.peersh/bridge');

  final FirebaseMessaging _fm;
  final MethodChannel _bridge;

  @override
  Future<bool> isReady() async {
    try {
      final settings = await _fm.getNotificationSettings();
      return settings.authorizationStatus == AuthorizationStatus.authorized ||
          settings.authorizationStatus == AuthorizationStatus.provisional;
    } catch (e) {
      debugPrint('peersh: FCM isReady failed: $e');
      return false;
    }
  }

  /// Requests notification permission. On Android 13+ this routes
  /// through MainActivity.requestNotifications via the platform
  /// channel (the bridge handles the runtime permission); on iOS we
  /// use firebase_messaging's request flow which surfaces the OS
  /// prompt directly.
  Future<bool> ensurePermission() async {
    try {
      if (Platform.isAndroid) {
        final granted =
            await _bridge.invokeMethod<bool>('requestNotifications') ?? false;
        return granted;
      }
      final settings = await _fm.requestPermission(
        alert: true,
        badge: true,
        sound: true,
      );
      return settings.authorizationStatus == AuthorizationStatus.authorized ||
          settings.authorizationStatus == AuthorizationStatus.provisional;
    } catch (e) {
      debugPrint('peersh: ensurePermission failed: $e');
      return false;
    }
  }

  @override
  Future<String> token() async {
    try {
      return await _fm.getToken() ?? '';
    } catch (e) {
      debugPrint('peersh: FCM getToken failed: $e');
      return '';
    }
  }

  @override
  Stream<RemoteMessage> get onMessage => FirebaseMessaging.onMessage;

  @override
  Stream<RemoteMessage> get onMessageOpenedApp =>
      FirebaseMessaging.onMessageOpenedApp;

  @override
  Future<RemoteMessage?> getInitialMessage() => _fm.getInitialMessage();

  @override
  Stream<String> get onTokenRefresh => _fm.onTokenRefresh;
}
