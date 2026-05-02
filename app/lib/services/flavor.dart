// Runtime detection of whether Firebase is wired up.
//
// peersh ships a single APK that supports both PSK and Firebase signaling
// servers. The mobile app always tries to initialize Firebase at startup;
// if the bundled `firebase_options.dart` is the placeholder stub, init
// throws and we run in PSK-only mode (Firebase server entries surface
// an error to the user). When the operator has run
// `flutterfire configure` and rebuilt, init succeeds and both modes
// work side by side.
//
// The companion file flavor_runtime.dart holds the mutable
// `firebaseInitialized` flag, set by main.dart after attempting init.

import 'flavor_runtime.dart' as r;

/// True when Firebase.initializeApp succeeded at startup. Determined at
/// runtime by main.dart after a try/catch around initializeApp.
bool get kFirebaseInitialized => r.firebaseInitialized;
