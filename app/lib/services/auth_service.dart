// Phase 5b scaffolding — pluggable auth backend.
//
// peersh's mobile app today only knows about PSK auth (operator-issued
// HMAC keys, paired with a `user` field on the server entry). Phase 5b
// will add a Firebase Auth backend so the official hosted signaling
// server can accept Google sign-in and surface a Firestore-backed
// device list.
//
// To keep the code path swappable, every screen that asks "who is the
// current user, and what credential do we present at Register time?"
// goes through this AuthService interface. The default implementation
// (PSKAuthService) reads the per-server PSK + user_id straight from
// the ServerEntry the way Phase 4b does. A future FirebaseAuthService
// will return a fresh ID token + the Firebase uid; the bridge layer
// will accept either kind.
//
// No Firebase import lives in this file by design. When Phase 5b
// flips on, the FirebaseAuthService implementation goes in a sibling
// file that does import firebase_auth, and the Riverpod provider
// below switches based on a runtime flag (see settings.dart).

import '../models/server_entry.dart';

/// Outcome of resolving credentials for a Register frame.
class AuthCredentials {
  const AuthCredentials.psk({required this.userId, required this.pskHex})
      : firebaseIdToken = null;

  const AuthCredentials.firebase({
    required this.userId,
    required this.firebaseIdToken,
  }) : pskHex = null;

  /// The user_id the server should bucket this device under. For PSK
  /// auth this is the operator-supplied string. For Firebase auth this
  /// is the Firebase uid.
  final String userId;

  /// Hex-encoded PSK, populated only for the PSK path.
  final String? pskHex;

  /// Firebase ID token, populated only for the Firebase path. Short-
  /// lived (~1 hour); callers should re-fetch before each Register.
  final String? firebaseIdToken;

  bool get isFirebase => firebaseIdToken != null;
}

/// Pluggable auth backend. The screens that build a server-entry-bound
/// session never reach for `firebase_auth` directly — they call
/// [resolve] on whichever AuthService the [authServiceProvider]
/// returns for this flavour of the app.
abstract class AuthService {
  /// Resolve credentials for the supplied server entry. Returns null
  /// when the operator has not finished setting up auth (e.g. Firebase
  /// sign-in flow not completed).
  Future<AuthCredentials?> resolve(ServerEntry server);
}

/// PSK implementation — used by every default APK build today.
class PskAuthService implements AuthService {
  const PskAuthService();

  @override
  Future<AuthCredentials?> resolve(ServerEntry server) async {
    if (server.userId.isEmpty || server.pskHex.isEmpty) return null;
    return AuthCredentials.psk(
      userId: server.userId,
      pskHex: server.pskHex,
    );
  }
}
