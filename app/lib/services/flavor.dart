// Compile-time toggle between PSK-only and PSK + Firebase builds.
//
// Phase 5b will replace this constant with a Dart conditional import
// (`firebase_flavor.dart`) plus an Android build flavor that wires up
// google-services.json. Until then, every release ships with PSK
// only and `kFirebaseEnabled` stays false.
//
// To turn Firebase on later:
//   1. Run `flutterfire configure` in `app/` to generate
//      `lib/firebase_options.dart` and android/app/google-services.json.
//   2. Add firebase_core / firebase_auth / cloud_firestore /
//      firebase_messaging / firebase_app_check to pubspec.yaml.
//   3. Replace this file's body with `const bool kFirebaseEnabled =
//      bool.fromEnvironment('PEERSH_FIREBASE');` and pass
//      `--dart-define=PEERSH_FIREBASE=true` to flutter build for the
//      firebase flavor.
//   4. Update services/auth_service.dart, device_discovery_service.dart,
//      fcm_service.dart to instantiate the FirebaseXxx implementation
//      when kFirebaseEnabled is true.

const bool kFirebaseEnabled = false;
