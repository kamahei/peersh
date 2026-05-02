// Sign-in helper that surfaces SignInScreen on top of the navigator
// when the user opens a Firebase server entry without an existing
// session, and returns a fresh ID token.

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'signin_screen.dart';

/// Ensures the user is signed in (showing SignInScreen if not) and
/// returns a fresh Firebase ID token. Returns null when the user
/// cancels.
Future<String?> ensureSignedInAndGetIdToken(
  BuildContext context,
  WidgetRef ref,
) async {
  final auth = ref.read(firebaseAuthServiceProvider);
  if (auth.currentUser == null) {
    final signedIn = await Navigator.of(context).push<bool>(
      MaterialPageRoute(builder: (_) => const SignInScreen(modal: true)),
    );
    if (signedIn != true || auth.currentUser == null) return null;
  }
  return auth.currentUser?.getIdToken(true);
}
