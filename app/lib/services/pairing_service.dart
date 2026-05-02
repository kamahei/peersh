// Phase 5b — Mobile-side pairing client.
//
// Calls the mintPairingCode Cloud Function with the user's Firebase ID
// token; the function mints a Custom Token for the caller's uid and
// returns a 6-digit code. The PC operator types the code into peershd
// (`peershd -pair-code 123456`), which exchanges it for the Custom
// Token and persists a long-lived Refresh Token. The mobile side never
// sees the Custom Token itself.

import 'dart:convert';

import 'package:firebase_core/firebase_core.dart';
import 'package:http/http.dart' as http;

class PairingCode {
  const PairingCode({
    required this.code,
    required this.expiresAt,
    required this.ttlSeconds,
  });

  final String code;
  final DateTime expiresAt;
  final int ttlSeconds;
}

class PairingService {
  PairingService({this.region = 'asia-northeast1', http.Client? client})
      : _client = client ?? http.Client();

  final String region;
  final http.Client _client;

  String get _projectId {
    final id = Firebase.app().options.projectId;
    if (id.isEmpty) {
      throw StateError('Firebase project id is empty; not configured.');
    }
    return id;
  }

  Uri _functionUri(String name) => Uri.parse(
        'https://$region-$_projectId.cloudfunctions.net/$name',
      );

  Future<PairingCode> mintCode({required String firebaseIdToken}) async {
    final res = await _client.post(
      _functionUri('mintPairingCode'),
      headers: {
        'authorization': 'Bearer $firebaseIdToken',
        'content-type': 'application/json',
      },
      body: '{}',
    );
    if (res.statusCode != 200) {
      throw StateError('mintPairingCode failed: ${res.statusCode} ${res.body}');
    }
    final body = jsonDecode(res.body) as Map<String, dynamic>;
    return PairingCode(
      code: body['code'] as String,
      expiresAt:
          DateTime.fromMillisecondsSinceEpoch(body['expires_at'] as int),
      ttlSeconds: (body['ttl_seconds'] as num).toInt(),
    );
  }
}
