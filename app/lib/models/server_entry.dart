/// A signaling server the user has registered with the app.
///
/// Persisted via [SecureStore]. Each entry holds its PSK secret as a hex
/// string; the secret never leaves device storage.
class ServerEntry {
  const ServerEntry({
    required this.id,
    required this.name,
    required this.wsUrl,
    required this.userId,
    required this.pskHex,
    required this.targetDeviceId,
    this.stunServer = 'stun.l.google.com:19302',
  });

  /// App-local identifier (UUID-ish; opaque).
  final String id;

  /// Free-form display name shown in lists.
  final String name;

  /// Full ws:// or wss:// URL of the signaling server's /ws endpoint.
  final String wsUrl;

  /// PSK user_id under which to register.
  final String userId;

  /// Hex-encoded PSK secret.
  final String pskHex;

  /// Default target peershd device_id for one-tap connect from the device
  /// list. The user pastes the host's device_id here when adding the
  /// server. Phase 4b is single-target per server; Phase 5 / Firebase
  /// will introduce real device discovery.
  final String targetDeviceId;

  /// STUN server hostname:port. Empty disables STUN.
  final String stunServer;

  ServerEntry copyWith({
    String? id,
    String? name,
    String? wsUrl,
    String? userId,
    String? pskHex,
    String? targetDeviceId,
    String? stunServer,
  }) =>
      ServerEntry(
        id: id ?? this.id,
        name: name ?? this.name,
        wsUrl: wsUrl ?? this.wsUrl,
        userId: userId ?? this.userId,
        pskHex: pskHex ?? this.pskHex,
        targetDeviceId: targetDeviceId ?? this.targetDeviceId,
        stunServer: stunServer ?? this.stunServer,
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'name': name,
        'wsUrl': wsUrl,
        'userId': userId,
        'pskHex': pskHex,
        'targetDeviceId': targetDeviceId,
        'stunServer': stunServer,
      };

  factory ServerEntry.fromJson(Map<String, dynamic> j) => ServerEntry(
        id: j['id'] as String,
        name: j['name'] as String,
        wsUrl: j['wsUrl'] as String,
        userId: j['userId'] as String,
        pskHex: j['pskHex'] as String,
        targetDeviceId: j['targetDeviceId'] as String? ?? '',
        stunServer: j['stunServer'] as String? ?? 'stun.l.google.com:19302',
      );
}
