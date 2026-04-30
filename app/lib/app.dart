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
