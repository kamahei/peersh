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
        _google = google ?? GoogleSignIn.instance;

  final FirebaseAuth _auth;
  final GoogleSignIn _google;

  bool _initialized = false;

  // google_sign_in v7 requires a one-shot initialize call before any
  // authenticate / signOut traffic. Android picks up the OAuth client
  // ID from google-services.json automatically; iOS reads GIDClientID
  // from Info.plist (configured by FlutterFire when the operator runs
  // `flutterfire configure`).
  Future<void> _ensureInitialized() async {
    if (_initialized) return;
    await _google.initialize();
    _initialized = true;
  }

  /// Returns the currently signed-in Firebase user, or null. Used by
  /// the sign-in screen to skip the prompt on relaunch.
  User? get currentUser => _auth.currentUser;

  /// Stream of auth state changes for UI to react to sign-in / out.
  Stream<User?> authStateChanges() => _auth.authStateChanges();

  /// Triggers the interactive Google sign-in flow and exchanges the
  /// returned ID token for a Firebase credential. Throws on
  /// cancellation or upstream errors.
  Future<User> signInWithGoogle() async {
    await _ensureInitialized();
    final GoogleSignInAccount account;
    try {
      account = await _google.authenticate();
    } on GoogleSignInException catch (e) {
      if (e.code == GoogleSignInExceptionCode.canceled) {
        throw StateError('Google sign-in was cancelled.');
      }
      rethrow;
    }
    final cred = GoogleAuthProvider.credential(
      idToken: account.authentication.idToken,
    );
    final result = await _auth.signInWithCredential(cred);
    final user = result.user;
    if (user == null) {
      throw StateError('Firebase auth returned a null user.');
    }
    return user;
  }

  Future<void> signOut() async {
    if (_initialized) {
      await _google.signOut();
    }
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
