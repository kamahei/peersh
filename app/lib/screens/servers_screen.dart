import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/server_entry.dart';
import '../services/flavor.dart' as flavor;
import '../services/notification_router.dart';
import '../state/servers.dart';
import 'device_picker_sheet.dart';
import 'firebase_signin.dart';
import 'server_editor_screen.dart';
import 'settings_screen.dart';
import 'signin_screen.dart';
import 'terminal_tabs_screen.dart';

class ServersScreen extends ConsumerStatefulWidget {
  const ServersScreen({super.key});

  @override
  ConsumerState<ServersScreen> createState() => _ServersScreenState();
}

class _ServersScreenState extends ConsumerState<ServersScreen> {
  bool _coldTapHandled = false;

  @override
  void initState() {
    super.initState();
    // Cold-start tap was seeded into the router by main.dart; consume
    // it after first frame so navigation happens once the screen is
    // mounted.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || _coldTapHandled) return;
      _coldTapHandled = true;
      _maybeRoute();
    });
  }

  Future<void> _maybeRoute() async {
    final pending = ref.read(notificationRouterProvider.notifier).consume();
    if (pending == null || !mounted) return;
    // Wait for the server list to load so we can match the host id.
    final serversAsync = await ref.read(serversProvider.future);
    if (!mounted) return;
    final match = serversAsync.firstWhere(
      (s) => s.targetDeviceId == pending.hostDeviceId,
      orElse: () => const ServerEntry(
        id: '',
        name: '',
        wsUrl: '',
        userId: '',
        pskHex: '',
        targetDeviceId: '',
      ),
    );
    if (match.id.isEmpty) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(
          'Notification target host ${pending.hostDeviceId} '
          'is not in your server list.',
        )),
      );
      return;
    }
    // Pop any deeper screens first so the user lands on the correct
    // server in a clean stack — otherwise tapping a notification while
    // already inside an unrelated TerminalTabsScreen would just stack
    // another one on top.
    await Navigator.of(context).pushAndRemoveUntil(
      MaterialPageRoute(
        builder: (_) => TerminalTabsScreen(
          server: match,
          pendingTabLabel: pending.tabLabel,
        ),
      ),
      (route) => route.isFirst,
    );
  }

  @override
  Widget build(BuildContext context) {
    // Listen for warm taps that arrive while the home screen is in the
    // foreground (e.g. user taps the notification while the app is
    // already on top). Cold-start case is handled in initState above.
    ref.listen<PendingNotification?>(notificationRouterProvider, (_, next) {
      if (next == null) return;
      _maybeRoute();
    });
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
    // Let the sign-in screen's pop transition (if any) settle before
    // opening the picker — back-to-back navigator ops trip
    // !_debugLocked.
    await WidgetsBinding.instance.endOfFrame;
    if (!context.mounted) return;
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
