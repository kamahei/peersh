// Phase 5b — placeholder Firebase options.
//
// Default (PSK) builds never call Firebase.initializeApp, so the
// throwing UnimplementedError below is never reached at runtime —
// the stub exists only so `import 'firebase_options.dart'` resolves
// at compile time without leaking project-specific configuration into
// git.
//
// To enable Firebase mode, regenerate this file locally:
//
//   cd app
//   flutterfire configure --project=<your-firebase-project-id>
//
// flutterfire writes real DefaultFirebaseOptions over this stub. To
// keep your project values from showing up in `git diff`, mark the
// file as locally modified:
//
//   git update-index --skip-worktree app/lib/firebase_options.dart
//
// Reverse with:
//
//   git update-index --no-skip-worktree app/lib/firebase_options.dart
//
// See docs/deploy/firebase.md for the full Phase 5b setup.

import 'package:firebase_core/firebase_core.dart';

class DefaultFirebaseOptions {
  static FirebaseOptions get currentPlatform {
    throw UnimplementedError(
      'firebase_options.dart is a placeholder. Run '
      '`flutterfire configure --project=<your-project>` from the app/ '
      'directory to generate real options for your Firebase project. '
      'Default (PSK) builds should never reach this code path because '
      'kFirebaseEnabled is false. See docs/deploy/firebase.md.',
    );
  }
}
