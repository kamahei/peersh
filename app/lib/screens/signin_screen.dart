// Phase 5b — Firebase Google sign-in screen.
//
// Surfaced from the connect flow when the user opens a server entry
// with `authMode = ServerAuthMode.firebase` and no existing Firebase
// session. With `modal: true` the screen pops with `true` on success
// (used as a navigator-pushed page from the connect flow).

import 'package:firebase_auth/firebase_auth.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../services/auth_service_firebase.dart';

final firebaseAuthServiceProvider = Provider<FirebaseAuthService>(
  (ref) => FirebaseAuthService(),
);

final authStateProvider = StreamProvider<User?>(
  (ref) => ref.watch(firebaseAuthServiceProvider).authStateChanges(),
);

class SignInScreen extends ConsumerStatefulWidget {
  const SignInScreen({super.key, this.modal = false});

  /// When true, the screen pops with `true` on successful sign-in
  /// (used as a navigator-pushed page from the connect flow).
  final bool modal;

  @override
  ConsumerState<SignInScreen> createState() => _SignInScreenState();
}

class _SignInScreenState extends ConsumerState<SignInScreen> {
  bool _busy = false;
  String? _error;

  Future<void> _signIn() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await ref.read(firebaseAuthServiceProvider).signInWithGoogle();
      if (!mounted) return;
      if (widget.modal) Navigator.of(context).pop(true);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: widget.modal ? AppBar(title: const Text('Sign in')) : null,
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.terminal_outlined, size: 96),
              const SizedBox(height: 16),
              Text(
                'peersh',
                style: Theme.of(context).textTheme.headlineMedium,
              ),
              const SizedBox(height: 8),
              const Text(
                'Sign in with Google to use a Firebase server.',
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 24),
              FilledButton.icon(
                onPressed: _busy ? null : _signIn,
                icon: const Icon(Icons.login),
                label: Text(_busy ? 'Signing in…' : 'Sign in with Google'),
              ),
              if (_error != null) ...[
                const SizedBox(height: 16),
                SelectableText(
                  _error!,
                  style: TextStyle(
                    color: Theme.of(context).colorScheme.error,
                    fontFamily: 'monospace',
                  ),
                  textAlign: TextAlign.center,
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}
