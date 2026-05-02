// Top-level app shell.
//
// The home is always ServersScreen. When the user opens a Firebase
// server entry and is not yet signed in, the connect flow surfaces a
// SignInScreen on top of the navigator (see TerminalTabsScreen).

import 'package:flutter/material.dart';

import 'screens/servers_screen.dart';

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
      home: const ServersScreen(),
    );
  }
}
