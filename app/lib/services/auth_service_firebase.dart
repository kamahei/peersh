// Phase 5b — Firebase-backed AuthService.
//
// Exchanges a Google sign-in credential for a Firebase ID token and
// hands that token back to the bridge layer, which forwards it to the
// signaling server's firebase auth provider.

import 'package:firebase_auth/firebase_auth.dart';
import 'package:google_sign_in/google_sign_in.dart';

import '../models/server_entry.dart';
import 'auth_service.dart';

class FirebaseAuthService implements AuthService {
  FirebaseAuthService({
    FirebaseAuth? auth,
    GoogleSignIn? google,
  })  : _auth = auth ?? FirebaseAuth.instance,
        _google = google ?? GoogleSignIn();

  final FirebaseAuth _auth;
  final GoogleSignIn _google;

  /// Returns the currently signed-in Firebase user, or null. Used by
  /// the sign-in screen to skip the prompt on relaunch.
  User? get currentUser => _auth.currentUser;

  /// Stream of auth state changes for UI to react to sign-in / out.
  Stream<User?> authStateChanges() => _auth.authStateChanges();

  /// Triggers the interactive Google sign-in flow and exchanges the
  /// returned ID token for a Firebase credential. Throws on
  /// cancellation or upstream errors.
  Future<User> signInWithGoogle() async {
    final account = await _google.signIn();
    if (account == null) {
      throw StateError('Google sign-in was cancelled.');
    }
    final googleAuth = await account.authentication;
    final cred = GoogleAuthProvider.credential(
      idToken: googleAuth.idToken,
      accessToken: googleAuth.accessToken,
    );
    final result = await _auth.signInWithCredential(cred);
    final user = result.user;
    if (user == null) {
      throw StateError('Firebase auth returned a null user.');
    }
    return user;
  }

  Future<void> signOut() async {
    await _google.signOut();
    await _auth.signOut();
  }

  @override
  Future<AuthCredentials?> resolve(ServerEntry server) async {
    final user = _auth.currentUser;
    if (user == null) return null;
    // Force a refresh so the token has plenty of validity for the
    // signaling Register frame (server tolerates up to 5 min skew).
    final token = await user.getIdToken(true);
    if (token == null || token.isEmpty) return null;
    return AuthCredentials.firebase(
      userId: user.uid,
      firebaseIdToken: token,
    );
  }
}
