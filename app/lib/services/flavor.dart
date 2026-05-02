// Compile-time toggle between PSK-only and PSK + Firebase builds.
//
// Set via dart-define at build time:
//   flutter build apk --debug --dart-define=PEERSH_FIREBASE=true
//
// Default (no dart-define) keeps the PSK-only path so an APK without
// google-services.json still builds and runs. The kFirebaseEnabled
// flag is read at runtime by the main app shell + Riverpod providers
// to pick between PskAuthService / FirebaseAuthService etc.

const bool kFirebaseEnabled =
    bool.fromEnvironment('PEERSH_FIREBASE', defaultValue: false);
