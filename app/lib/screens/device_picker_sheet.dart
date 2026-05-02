// Device picker bottom sheet for Firebase-mode servers.
//
// Surfaced from the connect flow when the user opens a Firebase server
// entry that has multiple registered hosts (or no remembered default).
// Reads `users/{uid}/devices` via FirebaseDeviceDiscoveryService and
// shows the host list sorted by last-seen.

import 'package:flutter/material.dart';

import '../models/server_entry.dart';
import '../services/device_discovery_service.dart';

/// Shows the device picker as a modal bottom sheet. Returns the chosen
/// deviceId or null if the user dismissed the sheet without picking.
Future<String?> showDevicePickerSheet({
  required BuildContext context,
  required ServerEntry server,
  required String uid,
}) {
  return showModalBottomSheet<String>(
    context: context,
    isScrollControlled: true,
    showDragHandle: true,
    builder: (ctx) => _DevicePickerSheet(server: server, uid: uid),
  );
}

class _DevicePickerSheet extends StatefulWidget {
  const _DevicePickerSheet({required this.server, required this.uid});

  final ServerEntry server;
  final String uid;

  @override
  State<_DevicePickerSheet> createState() => _DevicePickerSheetState();
}

class _DevicePickerSheetState extends State<_DevicePickerSheet> {
  late Future<List<DiscoveredDevice>> _future;

  @override
  void initState() {
    super.initState();
    _future = _load();
  }

  Future<List<DiscoveredDevice>> _load() {
    final svc = FirebaseDeviceDiscoveryService(uid: widget.uid);
    return svc.list(widget.server);
  }

  void _refresh() {
    setState(() => _future = _load());
  }

  @override
  Widget build(BuildContext context) {
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    'Pick a PC',
                    style: Theme.of(context).textTheme.titleMedium,
                  ),
                ),
                IconButton(
                  tooltip: 'Refresh',
                  onPressed: _refresh,
                  icon: const Icon(Icons.refresh),
                ),
              ],
            ),
            const SizedBox(height: 4),
            ConstrainedBox(
              constraints: BoxConstraints(
                maxHeight: MediaQuery.of(context).size.height * 0.6,
              ),
              child: FutureBuilder<List<DiscoveredDevice>>(
                future: _future,
                builder: (ctx, snap) {
                  if (snap.connectionState != ConnectionState.done) {
                    return const Padding(
                      padding: EdgeInsets.all(32),
                      child: Center(child: CircularProgressIndicator()),
                    );
                  }
                  if (snap.hasError) {
                    return Padding(
                      padding: const EdgeInsets.all(16),
                      child: SelectableText(
                        '${snap.error}',
                        style: TextStyle(
                          color: Theme.of(context).colorScheme.error,
                          fontFamily: 'monospace',
                        ),
                      ),
                    );
                  }
                  final devices = snap.data ?? const [];
                  if (devices.isEmpty) {
                    return const Padding(
                      padding: EdgeInsets.symmetric(vertical: 32),
                      child: Text(
                        'No PCs registered yet.\nRun peershd on the host '
                        'to register it under your account.',
                        textAlign: TextAlign.center,
                      ),
                    );
                  }
                  return ListView.separated(
                    shrinkWrap: true,
                    itemCount: devices.length,
                    separatorBuilder: (_, __) => const Divider(height: 1),
                    itemBuilder: (lctx, i) {
                      final d = devices[i];
                      final isCurrent =
                          d.deviceId == widget.server.targetDeviceId;
                      return ListTile(
                        leading: Icon(
                          Icons.computer,
                          color: isCurrent
                              ? Theme.of(context).colorScheme.primary
                              : null,
                        ),
                        title: Text(d.displayName),
                        subtitle: Text(
                          _subtitle(d),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                        ),
                        trailing: isCurrent
                            ? const Icon(Icons.check)
                            : const Icon(Icons.chevron_right),
                        onTap: () => Navigator.of(ctx).pop(d.deviceId),
                      );
                    },
                  );
                },
              ),
            ),
          ],
        ),
      ),
    );
  }

  String _subtitle(DiscoveredDevice d) {
    final parts = <String>[d.deviceId];
    if (d.lastSeenUnixMs > 0) {
      final delta = DateTime.now().millisecondsSinceEpoch - d.lastSeenUnixMs;
      parts.add('seen ${_humanizeDelta(delta)}');
    }
    return parts.join(' · ');
  }

  String _humanizeDelta(int ms) {
    if (ms < 60000) return 'just now';
    if (ms < 3600000) return '${ms ~/ 60000} min ago';
    if (ms < 86400000) return '${ms ~/ 3600000} h ago';
    return '${ms ~/ 86400000} d ago';
  }
}
