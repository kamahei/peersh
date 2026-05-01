// Phase 5b scaffolding — FCM wake-up registration.
//
// In Firebase mode, peershd registers its own FCM token onto the
// `users/{uid}/devices/{deviceId}` Firestore document so the
// signaling server's onSessionCreated Cloud Function knows where to
// send the wake-up push. The mobile app uses Cloud Messaging only as
// a recipient of those pushes — but it also needs to surface push
// permission to the user (Android 13+).
//
// Today this stub does nothing. When Phase 5b activates, the
// FirebaseFcmService implementation will:
//   1. Request notification permission via firebase_messaging.
//   2. Read the device token via FirebaseMessaging.instance.getToken().
//   3. Surface incoming push events so the app can foreground a
//      session screen if the user taps the notification.

abstract class FcmService {
  /// Returns true when the OS / user has granted notification
  /// permission AND a Firebase project is configured.
  Future<bool> isReady();

  /// Optionally returns a current FCM token. Empty when unconfigured.
  Future<String> token();
}

class NoopFcmService implements FcmService {
  const NoopFcmService();

  @override
  Future<bool> isReady() async => false;

  @override
  Future<String> token() async => '';
}
