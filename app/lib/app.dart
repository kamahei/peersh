// Top-level app shell.
//
// The home is always ServersScreen. When the user opens a Firebase
// server entry and is not yet signed in, the connect flow surfaces a
// SignInScreen on top of the navigator (see TerminalTabsScreen).

import 'dart:async';

import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'screens/servers_screen.dart';
import 'services/fcm_service.dart';
import 'services/notification_router.dart';

class PeershApp extends ConsumerStatefulWidget {
  const PeershApp({super.key, this.fcm});

  /// Concrete FCM service when Firebase is enabled. Null in PSK-only
  /// builds; the warm-tap subscription is then a no-op.
  final FirebaseFcmService? fcm;

  @override
  ConsumerState<PeershApp> createState() => _PeershAppState();
}

class _PeershAppState extends ConsumerState<PeershApp> {
  StreamSubscription<RemoteMessage>? _openedAppSub;

  @override
  void initState() {
    super.initState();
    final fcm = widget.fcm;
    if (fcm != null) {
      _openedAppSub = fcm.onMessageOpenedApp.listen((m) {
        final pending = PendingNotification.fromMessage(m);
        if (pending == null) return;
        ref.read(notificationRouterProvider.notifier).set(pending);
      });
    }
  }

  @override
  void dispose() {
    _openedAppSub?.cancel();
    super.dispose();
  }

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
      home: const ServersScreen(),
    );
  }
}
