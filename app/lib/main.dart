// peersh mobile app entry point.
//
// Always attempts to initialize Firebase. If `firebase_options.dart` is
// the placeholder stub (default OSS source), Firebase.initializeApp
// throws and we set kFirebaseInitialized = false so the app runs in
// PSK-only mode. When the operator has run `flutterfire configure`,
// initialization succeeds and Firebase server entries become usable
// alongside PSK ones.

import 'dart:async';

import 'package:firebase_app_check/firebase_app_check.dart';
import 'package:firebase_auth/firebase_auth.dart';
import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app.dart';
import 'firebase_options.dart';
import 'services/fcm_service.dart';
import 'services/flavor_runtime.dart';
import 'services/mobile_device_registry.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  try {
    await Firebase.initializeApp(
      options: DefaultFirebaseOptions.currentPlatform,
    );
    firebaseInitialized = true;
    // App Check ships in every Firebase-enabled APK. The server-side
    // `app_check_required` config decides whether to enforce; rolling
    // tokens out on clients first keeps the deployment order safe.
    try {
      await FirebaseAppCheck.instance.activate(
        providerAndroid: kReleaseMode
            ? const AndroidPlayIntegrityProvider()
            : const AndroidDebugProvider(),
      );
    } catch (e) {
      debugPrint('peersh: App Check activation failed (continuing): $e');
    }
    // v2-B push notifications: request OS notification permission,
    // and (on every sign-in event) keep the mobile FCM token registered
    // under users/{uid}/devices/{mobileDeviceId} so the host can
    // address us. This block is fire-and-forget; failures degrade
    // gracefully (no notifications, but the rest of the app works).
    final fcm = FirebaseFcmService();
    unawaited(fcm.ensurePermission());
    final registry = MobileDeviceRegistry(fcm: fcm);
    FirebaseAuth.instance.authStateChanges().listen((user) {
      if (user == null) {
        unawaited(registry.dispose());
        return;
      }
      unawaited(registry.register(user.uid));
    });
  } catch (e) {
    firebaseInitialized = false;
    debugPrint('peersh: Firebase initialization skipped (PSK-only mode): $e');
  }
  runApp(const ProviderScope(child: PeershApp()));
}
