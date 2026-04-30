import 'dart:convert';

import 'package:http/http.dart' as http;

/// Result of fetching `/.well-known/peersh.json` from a hostname.
class DiscoveryDoc {
  const DiscoveryDoc({
    required this.version,
    required this.wsUrl,
    required this.stunServers,
    required this.authProviders,
  });

  final int version;
  final String wsUrl;
  final List<String> stunServers;
  final List<String> authProviders;

  factory DiscoveryDoc.fromJson(Map<String, dynamic> j) => DiscoveryDoc(
        version: (j['version'] as num? ?? 1).toInt(),
        wsUrl: j['ws_url'] as String? ?? '',
        stunServers:
            (j['stun_servers'] as List? ?? const []).cast<String>(),
        authProviders:
            (j['auth_providers'] as List? ?? const []).cast<String>(),
      );
}

/// Fetches the discovery document.
///
/// Accepts either a bare hostname ("signaling.example.com"), an
/// https://... URL, or http://... URL with explicit port. Returns null
/// if discovery fails (caller falls back to manually-entered values).
Future<DiscoveryDoc?> fetchDiscovery(String input,
    {Duration timeout = const Duration(seconds: 5)}) async {
  final url = _resolveDiscoveryUrl(input);
  if (url == null) return null;
  try {
    final resp = await http.get(url).timeout(timeout);
    if (resp.statusCode != 200) return null;
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    return DiscoveryDoc.fromJson(body);
  } catch (_) {
    return null;
  }
}

Uri? _resolveDiscoveryUrl(String input) {
  final trimmed = input.trim();
  if (trimmed.isEmpty) return null;
  // 1) full URL provided.
  if (trimmed.startsWith('http://') || trimmed.startsWith('https://')) {
    final parsed = Uri.tryParse(trimmed);
    if (parsed == null) return null;
    return parsed.replace(path: '/.well-known/peersh.json', query: '');
  }
  // 2) host[:port] — prefer https when no port is set.
  final hasPort = trimmed.contains(':');
  final scheme = hasPort ? 'http' : 'https';
  return Uri.parse('$scheme://$trimmed/.well-known/peersh.json');
}
