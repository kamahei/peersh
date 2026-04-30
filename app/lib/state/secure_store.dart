import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import '../models/server_entry.dart';

/// Persistent server-list storage. Backed by [FlutterSecureStorage] so
/// PSK secrets sit in the OS-level secure store (Android Keystore / iOS
/// Keychain) rather than plain SharedPreferences.
class SecureStore {
  SecureStore({FlutterSecureStorage? storage}) : _storage = storage ?? _defaultStorage;

  static const _serversKey = 'servers.v1';
  static const _settingsKey = 'settings.v1';

  static const _defaultStorage = FlutterSecureStorage(
    aOptions: AndroidOptions(encryptedSharedPreferences: true),
    iOptions: IOSOptions(accessibility: KeychainAccessibility.first_unlock),
  );

  final FlutterSecureStorage _storage;

  Future<List<ServerEntry>> readServers() async {
    final raw = await _storage.read(key: _serversKey);
    if (raw == null || raw.isEmpty) return const [];
    final list = jsonDecode(raw) as List;
    return list
        .cast<Map<String, dynamic>>()
        .map(ServerEntry.fromJson)
        .toList(growable: false);
  }

  Future<void> writeServers(List<ServerEntry> entries) async {
    final encoded = jsonEncode(entries.map((e) => e.toJson()).toList());
    await _storage.write(key: _serversKey, value: encoded);
  }

  Future<Map<String, dynamic>> readSettings() async {
    final raw = await _storage.read(key: _settingsKey);
    if (raw == null || raw.isEmpty) return const {};
    return (jsonDecode(raw) as Map).cast<String, dynamic>();
  }

  Future<void> writeSettings(Map<String, dynamic> settings) async {
    await _storage.write(key: _settingsKey, value: jsonEncode(settings));
  }
}
