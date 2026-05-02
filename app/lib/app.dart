// Top-level app shell.
//
// In PSK-only builds (default), the home is the ServersScreen.
// In Firebase-enabled builds (kFirebaseEnabled = true), the home
// listens to FirebaseAuth.authStateChanges and routes to either the
// SignInScreen or the ServersScreen based on whether the user is
// signed in.

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'screens/servers_screen.dart';
import 'screens/signin_screen.dart';
import 'services/flavor.dart';

class PeershApp extends StatelessWidget {
  const PeershApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'peersh',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: Colors.indigo),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: Colors.indigo,
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      home: kFirebaseEnabled ? const _FirebaseGate() : const ServersScreen(),
    );
  }
}

/// Firebase auth-state gate. Shows SignInScreen when signed out and
/// ServersScreen when signed in. Listens to FirebaseAuth's stream so
/// signing in / out automatically swaps screens.
class _FirebaseGate extends ConsumerWidget {
  const _FirebaseGate();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final auth = ref.watch(authStateProvider);
    return auth.when(
      data: (user) =>
          user == null ? const SignInScreen() : const ServersScreen(),
      loading: () => const Scaffold(
        body: Center(child: CircularProgressIndicator()),
      ),
      error: (e, _) => Scaffold(
        body: Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: SelectableText('auth error: $e'),
          ),
        ),
      ),
    );
  }
}
