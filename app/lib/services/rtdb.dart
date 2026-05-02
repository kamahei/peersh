// Realtime Database access helper.
//
// FlutterFire's generated firebase_options.dart does not (yet) include
// the databaseURL field for this project, so FirebaseDatabase.instance
// would not know which RTDB instance to talk to. We construct the URL
// from the active app's projectId plus a known region.
//
// If a future `flutterfire configure` adds databaseURL to
// FirebaseOptions, this helper still works — it just overrides with
// the explicit URL, which is harmless.

import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_database/firebase_database.dart';

// Realtime Database is created in this region (Firebase Console at
// project setup time). asia-northeast1 is not a valid RTDB region;
// asia-southeast1 (Singapore) is the closest option for jp users.
const _rtdbRegion = 'asia-southeast1';

/// Returns the FirebaseDatabase instance for the default app, with
/// databaseURL constructed from the project id + the RTDB region.
FirebaseDatabase get peershDatabase {
  final app = Firebase.app();
  final projectId = app.options.projectId;
  final url =
      'https://$projectId-default-rtdb.$_rtdbRegion.firebasedatabase.app';
  return FirebaseDatabase.instanceFor(app: app, databaseURL: url);
}
