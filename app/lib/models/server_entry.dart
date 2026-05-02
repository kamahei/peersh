// A signaling server the user has registered with the app.
//
// Persisted via [SecureStore]. Each entry holds its PSK secret as a hex
// string (when [authMode] == [ServerAuthMode.psk]); the secret never
// leaves device storage. When [authMode] == [ServerAuthMode.firebase],
// the app forwards a fresh Firebase ID token from `firebase_auth`
// instead and the PSK / userId fields are ignored at connect time.

/// How the app authenticates against this signaling server. Picks the
/// signaling-side auth provider; both modes coexist in a single APK.
enum ServerAuthMode {
  psk,
  firebase,
}

class ServerEntry {
  const ServerEntry({
    required this.id,
    required this.name,
    required this.wsUrl,
    required this.userId,
    required this.pskHex,
    required this.targetDeviceId,
    this.stunServer = 'stun.l.google.com:19302',
    this.authMode = ServerAuthMode.psk,
  });

  /// App-local identifier (UUID-ish; opaque).
  final String id;

  /// Free-form display name shown in lists.
  final String name;

  /// Full ws:// or wss:// URL of the signaling server's /ws endpoint.
  final String wsUrl;

  /// PSK user_id under which to register. Ignored in firebase mode.
  final String userId;

  /// Hex-encoded PSK secret. Ignored in firebase mode.
  final String pskHex;

  /// Default target peershd device_id for one-tap connect from the device
  /// list. The user pastes the host's device_id here when adding the
  /// server. Phase 4b is single-target per server; Phase 5 / Firebase
  /// will introduce real device discovery.
  final String targetDeviceId;

  /// STUN server hostname:port. Empty disables STUN.
  final String stunServer;

  /// Which auth provider this server expects.
  final ServerAuthMode authMode;

  ServerEntry copyWith({
    String? id,
    String? name,
    String? wsUrl,
    String? userId,
    String? pskHex,
    String? targetDeviceId,
    String? stunServer,
    ServerAuthMode? authMode,
  }) =>
      ServerEntry(
        id: id ?? this.id,
        name: name ?? this.name,
        wsUrl: wsUrl ?? this.wsUrl,
        userId: userId ?? this.userId,
        pskHex: pskHex ?? this.pskHex,
        targetDeviceId: targetDeviceId ?? this.targetDeviceId,
        stunServer: stunServer ?? this.stunServer,
        authMode: authMode ?? this.authMode,
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'name': name,
        'wsUrl': wsUrl,
        'userId': userId,
        'pskHex': pskHex,
        'targetDeviceId': targetDeviceId,
        'stunServer': stunServer,
        'authMode': authMode.name,
      };

  factory ServerEntry.fromJson(Map<String, dynamic> j) => ServerEntry(
        id: j['id'] as String,
        name: j['name'] as String,
        wsUrl: j['wsUrl'] as String,
        userId: j['userId'] as String? ?? '',
        pskHex: j['pskHex'] as String? ?? '',
        targetDeviceId: j['targetDeviceId'] as String? ?? '',
        stunServer: j['stunServer'] as String? ?? 'stun.l.google.com:19302',
        authMode: _parseAuthMode(j['authMode']),
      );
}

ServerAuthMode _parseAuthMode(Object? raw) {
  if (raw is String) {
    for (final mode in ServerAuthMode.values) {
      if (mode.name == raw) return mode;
    }
  }
  return ServerAuthMode.psk;
}
