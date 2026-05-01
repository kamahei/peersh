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

/// Outcome of a discovery attempt. Either [DiscoveryDoc] on success or a
/// short human-readable [error] string the editor can show to the user.
class DiscoveryResult {
  const DiscoveryResult({this.doc, this.error});
  final DiscoveryDoc? doc;
  final String? error;
  bool get isOk => doc != null;
}

/// Fetches the discovery document.
///
/// Accepts either a bare hostname ("signaling.example.com"), an
/// https://... URL, or http://... URL with explicit port. On failure the
/// returned result holds a short [error] string explaining why.
Future<DiscoveryResult> fetchDiscovery(String input,
    {Duration timeout = const Duration(seconds: 10)}) async {
  final url = _resolveDiscoveryUrl(input);
  if (url == null) {
    return const DiscoveryResult(error: 'Invalid hostname or URL.');
  }
  try {
    final resp = await http.get(url).timeout(timeout);
    if (resp.statusCode != 200) {
      return DiscoveryResult(
          error: 'HTTP ${resp.statusCode} from ${url.host}${url.path}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    return DiscoveryResult(doc: DiscoveryDoc.fromJson(body));
  } catch (e) {
    return DiscoveryResult(error: 'Lookup failed: $e');
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
