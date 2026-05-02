// peersh mobile app entry point.
//
// When the build is configured with --dart-define=PEERSH_FIREBASE=true,
// initialize Firebase before runApp; otherwise the firebase_options
// import is still safe (it ships in every build because google-services
// is wired in for the firebase flavor) but Firebase.initializeApp is
// skipped so PSK-only deployments don't require a live Firebase
// project.

import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'app.dart';
import 'firebase_options.dart';
import 'services/flavor.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (kFirebaseEnabled) {
    await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
  }
  runApp(const ProviderScope(child: PeershApp()));
}
