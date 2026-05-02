// peersh mobile app entry point.
//
// Always attempts to initialize Firebase. If `firebase_options.dart` is
// the placeholder stub (default OSS source), Firebase.initializeApp
// throws and we set kFirebaseInitialized = false so the app runs in
// PSK-only mode. When the operator has run `flutterfire configure`,
// initialization succeeds and Firebase server entries become usable
// alongside PSK ones.

import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app.dart';
import 'firebase_options.dart';
import 'services/flavor_runtime.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  try {
    await Firebase.initializeApp(
      options: DefaultFirebaseOptions.currentPlatform,
    );
    firebaseInitialized = true;
  } catch (e) {
    firebaseInitialized = false;
    debugPrint('peersh: Firebase initialization skipped (PSK-only mode): $e');
  }
  runApp(const ProviderScope(child: PeershApp()));
}
