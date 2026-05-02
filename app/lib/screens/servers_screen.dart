import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/server_entry.dart';
import '../services/flavor.dart' as flavor;
import '../state/servers.dart';
import 'device_picker_sheet.dart';
import 'firebase_signin.dart';
import 'server_editor_screen.dart';
import 'settings_screen.dart';
import 'signin_screen.dart';
import 'terminal_tabs_screen.dart';

class ServersScreen extends ConsumerWidget {
  const ServersScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final serversAsync = ref.watch(serversProvider);
    return Scaffold(
      appBar: AppBar(
        title: const Text('peersh'),
        actions: [
          IconButton(
            tooltip: 'Settings',
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute(builder: (_) => const SettingsScreen()),
            ),
            icon: const Icon(Icons.settings_outlined),
          ),
        ],
      ),
      body: serversAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: SelectableText('$e')),
        data: (servers) => servers.isEmpty
            ? const _EmptyState()
            : ListView.separated(
                itemCount: servers.length,
                separatorBuilder: (_, __) => const Divider(height: 1),
                itemBuilder: (ctx, i) {
                  final s = servers[i];
                  return Dismissible(
                    key: ValueKey('server-${s.id}'),
                    direction: DismissDirection.endToStart,
                    background: Container(
                      alignment: Alignment.centerRight,
                      padding: const EdgeInsets.symmetric(horizontal: 24),
                      color: Colors.red,
                      child: const Icon(Icons.delete, color: Colors.white),
                    ),
                    confirmDismiss: (_) async {
                      return await showDialog<bool>(
                            context: ctx,
                            builder: (dctx) => AlertDialog(
                              title: const Text('Delete server?'),
                              content: Text('Remove "${s.name}"?'),
                              actions: [
                                TextButton(
                                  onPressed: () => Navigator.pop(dctx, false),
                                  child: const Text('Cancel'),
                                ),
                                TextButton(
                                  onPressed: () => Navigator.pop(dctx, true),
                                  child: const Text('Delete'),
                                ),
                              ],
                            ),
                          ) ??
                          false;
                    },
                    onDismissed: (_) =>
                        ref.read(serversProvider.notifier).remove(s.id),
                    child: ListTile(
                      title: Text(s.name),
                      subtitle: Text(
                        _serverSubtitle(s),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                      onTap: () => Navigator.of(context).push(
                        MaterialPageRoute(
                          builder: (_) => TerminalTabsScreen(server: s),
                        ),
                      ),
                      trailing: PopupMenuButton<String>(
                        icon: const Icon(Icons.more_vert),
                        onSelected: (v) async {
                          if (v == 'edit') {
                            await Navigator.of(context).push(
                              MaterialPageRoute(
                                builder: (_) =>
                                    ServerEditorScreen(existing: s),
                              ),
                            );
                          } else if (v == 'switch_device') {
                            await _switchDevice(context, ref, s);
                          }
                        },
                        itemBuilder: (_) => [
                          const PopupMenuItem(
                            value: 'edit',
                            child: ListTile(
                              leading: Icon(Icons.edit_outlined),
                              title: Text('Edit'),
                            ),
                          ),
                          if (s.authMode == ServerAuthMode.firebase &&
                              flavor.kFirebaseInitialized)
                            const PopupMenuItem(
                              value: 'switch_device',
                              child: ListTile(
                                leading: Icon(Icons.computer),
                                title: Text('Switch PC'),
                              ),
                            ),
                        ],
                      ),
                    ),
                  );
                },
              ),
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => Navigator.of(context).push(
          MaterialPageRoute(builder: (_) => const ServerEditorScreen()),
        ),
        icon: const Icon(Icons.add),
        label: const Text('Add server'),
      ),
    );
  }

  static String _serverSubtitle(ServerEntry s) {
    switch (s.authMode) {
      case ServerAuthMode.firebase:
        if (s.targetDeviceId.isNotEmpty) {
          return 'firebase · ${s.targetDeviceId}';
        }
        return 'firebase · pick a PC on connect';
      case ServerAuthMode.psk:
        return '${s.userId} @ ${s.wsUrl}';
    }
  }

  Future<void> _switchDevice(
    BuildContext context,
    WidgetRef ref,
    ServerEntry server,
  ) async {
    final idToken = await ensureSignedInAndGetIdToken(context, ref);
    if (idToken == null) return;
    final user = ref.read(firebaseAuthServiceProvider).currentUser;
    if (user == null || !context.mounted) return;
    final picked = await showDevicePickerSheet(
      context: context,
      server: server,
      uid: user.uid,
    );
    if (picked == null || !context.mounted) return;
    await ref
        .read(serversProvider.notifier)
        .replace(server.copyWith(targetDeviceId: picked));
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState();

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(24),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          const Icon(Icons.dns_outlined, size: 64),
          const SizedBox(height: 16),
          Text(
            'No servers yet',
            style: Theme.of(context).textTheme.titleMedium,
          ),
          const SizedBox(height: 8),
          const Text(
            'Tap "Add server" and paste a peersh-signaling URL plus the '
            'PSK your operator provided. The host\'s device_id is printed '
            'on peershd start-up.',
            textAlign: TextAlign.center,
          ),
        ],
      ),
    );
  }
}

/// Helper used by widget tests to build an unused [ServerEntry].
ServerEntry sampleServerEntry() => const ServerEntry(
      id: 'sample',
      name: 'sample',
      wsUrl: 'ws://localhost:8443/ws',
      userId: 'alice',
      pskHex: '00',
      targetDeviceId: 'AAAAAAAAAAAAAAAA',
    );
