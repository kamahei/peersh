// Phase 6b Tier 1 — multi-tab terminal screen.
//
// Hosts a list of TerminalTabModel/TerminalPane pairs against ONE
// PeershSession (one QUIC connection). Each tab owns its own xterm
// Terminal + PTY session id; the panes are kept alive via IndexedStack
// so a backgrounded tab keeps streaming output into its scrollback.
//
// PTY reattach across reconnects (and matching server-side persistence
// + scrollback ring buffer) is Tier 2 of Phase 6b.

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/pty_event.dart';
import '../models/pty_file.dart';
import '../models/server_entry.dart';
import '../services/flavor.dart' as flavor;
import '../services/peersh_session.dart';
import '../state/persisted_pty_handles.dart';
import '../state/servers.dart';
import '../state/settings.dart';
import '../widgets/special_keys_bar.dart';
import 'device_picker_sheet.dart';
import 'file_browser_screen.dart';
import 'firebase_signin.dart';
import 'ime_input_sheet.dart';
import 'signin_screen.dart';
import 'terminal_pane.dart';
import 'text_viewer_screen.dart';

class TerminalTabsScreen extends ConsumerStatefulWidget {
  const TerminalTabsScreen({super.key, required this.server});

  final ServerEntry server;

  @override
  ConsumerState<TerminalTabsScreen> createState() =>
      _TerminalTabsScreenState();
}

enum _ConnState { connecting, connected, reconnecting, error }

class _TerminalTabsScreenState extends ConsumerState<TerminalTabsScreen> {
  PeershSession? _session;
  String? _connectError;
  final List<TerminalTabModel> _tabs = [];
  int _activeIndex = 0;
  int _tabSeq = 1;

  // Reconnect bookkeeping. _connState drives the spinner / banner; the
  // backoff schedule is base * 2^attempt, capped at _maxBackoff.
  _ConnState _connState = _ConnState.connecting;
  int _reconnectAttempts = 0;
  Timer? _reconnectTimer;
  StreamSubscription<PtyEvent>? _ptyExitWatcher;
  bool _probingDisconnect = false;
  ServerEntry? _resolvedServer; // server with picked device id baked in
  bool _showResumedBanner = false;
  Timer? _resumedBannerTimer;
  static const _maxReconnectAttempts = 6;
  static const _baseBackoff = Duration(milliseconds: 500);
  static const _maxBackoff = Duration(seconds: 30);

  @override
  void initState() {
    super.initState();
    // Defer the connect-flow's first navigator op (sign-in screen,
    // device picker) until after this screen's own push transition
    // settles — otherwise nested Navigator.push during transition
    // trips the !_debugLocked assertion.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _connectSession();
    });
    // Screen-wide observer of PTY exits. When an exit lands with a
    // non-empty error, a panel-level closePty hasn't been issued, and
    // the connection state is "connected", probe the session to decide
    // between "child died on its own" and "QUIC dropped".
    _ptyExitWatcher = ref.read(bridgeProvider).ptyEvents().listen((evt) {
      if (evt is PtyExitEvent && evt.isError) {
        _probeOrReconnect();
      }
    });
  }

  Future<void> _connectSession({bool isReconnect = false}) async {
    final bridge = ref.read(bridgeProvider);
    try {
      String? idToken;
      ServerEntry connectServer = _resolvedServer ?? widget.server;
      if (widget.server.authMode == ServerAuthMode.firebase) {
        if (!flavor.kFirebaseInitialized) {
          throw StateError(
            'Firebase server entry but Firebase is not initialized in this APK. '
            'Run `flutterfire configure` and rebuild to enable Firebase mode.',
          );
        }
        idToken = await ensureSignedInAndGetIdToken(context, ref);
        if (idToken == null) {
          throw StateError('Sign-in cancelled.');
        }
        // Picker only fires on the very first connect to a Firebase
        // entry that has no remembered device. Reconnect uses the
        // already-resolved server.
        if (!isReconnect && connectServer.targetDeviceId.isEmpty) {
          await WidgetsBinding.instance.endOfFrame;
          if (!mounted) return;
          final picked = await _pickDevice(connectServer);
          if (picked == null) throw StateError('No PC selected.');
          connectServer = connectServer.copyWith(targetDeviceId: picked);
          await ref.read(serversProvider.notifier).replace(connectServer);
        }
      }
      _resolvedServer = connectServer;
      final session = await PeershSession.open(
        bridge: bridge,
        server: connectServer,
        firebaseIdToken: idToken,
      );
      if (!mounted) {
        await session.close();
        return;
      }
      // Probe persisted PTYs the host is holding for this connection.
      // If any are present (and not already attached to another tab),
      // surface a reattach prompt instead of auto-spawning a fresh tab.
      List<PtyHandleInfo> persisted = const [];
      try {
        persisted = await bridge.listPtys(sessionId: session.id);
      } catch (_) {}
      setState(() {
        _session = session;
        _connState = _ConnState.connected;
        _reconnectAttempts = 0;
        _connectError = null;
        if (_tabs.isNotEmpty) return;
        if (persisted.where((p) => !p.attached).isEmpty) {
          // No reattachable PTYs — auto-spawn a fresh shell.
          _tabs.add(TerminalTabModel(initialLabel: 'shell'));
        }
        // Otherwise leave _tabs empty so the user picks via the prompt.
      });
      if (isReconnect) _showReattachBanner();
      if (mounted && _tabs.isEmpty) {
        await _maybeOfferReattach(persisted);
      }
    } catch (e) {
      if (!mounted) return;
      _scheduleReconnectOrFail('$e');
    }
  }

  /// On a connect failure, schedule the next backoff attempt unless we
  /// have exhausted [_maxReconnectAttempts]. Some errors (sign-in
  /// cancelled, no PC selected) are user-driven and don't deserve a
  /// retry storm — those go straight to the manual error state.
  void _scheduleReconnectOrFail(String error) {
    final isUserCancel = error.contains('Sign-in cancelled') ||
        error.contains('No PC selected');
    if (isUserCancel || _reconnectAttempts >= _maxReconnectAttempts) {
      setState(() {
        _connState = _ConnState.error;
        _connectError = error;
      });
      return;
    }
    _reconnectAttempts++;
    final shift = _reconnectAttempts - 1;
    var delay = _baseBackoff * (1 << shift);
    if (delay > _maxBackoff) delay = _maxBackoff;
    setState(() {
      _connState = _ConnState.reconnecting;
      _connectError = error;
    });
    _reconnectTimer?.cancel();
    _reconnectTimer = Timer(delay, () {
      if (mounted) _connectSession(isReconnect: true);
    });
  }

  void _showReattachBanner() {
    setState(() => _showResumedBanner = true);
    _resumedBannerTimer?.cancel();
    _resumedBannerTimer = Timer(const Duration(seconds: 4), () {
      if (mounted) setState(() => _showResumedBanner = false);
    });
  }

  /// Cancel the current backoff loop and surface the last error so the
  /// user can manually retry from the error screen.
  void _cancelReconnect() {
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    setState(() {
      _connState = _ConnState.error;
    });
  }

  /// Probe the existing session via a cheap RPC. If the call fails, the
  /// QUIC connection is presumed dead — tear down and start the
  /// reconnect loop. Called from the screen-wide PTY observer when an
  /// exit event with a non-empty error arrives.
  Future<void> _probeOrReconnect() async {
    final session = _session;
    if (session == null || _probingDisconnect) return;
    if (_connState != _ConnState.connected) return;
    _probingDisconnect = true;
    try {
      await ref
          .read(bridgeProvider)
          .listPtys(sessionId: session.id)
          .timeout(const Duration(seconds: 5));
    } catch (e) {
      // Connection is dead — drop the session and try to reconnect.
      _session = null;
      try {
        await session.close();
      } catch (_) {}
      // Detach all tabs from their PTY ids so the next connect either
      // reattaches or spawns fresh shells.
      for (final tab in _tabs) {
        tab.ptyId = null;
        // Keep the reattach handle so the host's persisted PTY (if
        // any) will be picked up by openPty's reattach branch on the
        // next attempt.
      }
      if (!mounted) return;
      _scheduleReconnectOrFail('connection dropped: $e');
    } finally {
      _probingDisconnect = false;
    }
  }

  Future<String?> _pickDevice(ServerEntry server) async {
    final user = ref.read(firebaseAuthServiceProvider).currentUser;
    if (user == null) return null;
    if (!mounted) return null;
    return showDevicePickerSheet(
      context: context,
      server: server,
      uid: user.uid,
    );
  }

  Future<void> _maybeOfferReattach(List<PtyHandleInfo> persisted) async {
    final reattachable =
        persisted.where((p) => !p.attached).toList(growable: false);
    if (reattachable.isEmpty || !mounted) {
      // Edge case: persisted but all attached. Just open a fresh tab.
      setState(() => _tabs.add(TerminalTabModel(initialLabel: 'shell')));
      return;
    }
    final picked = await showModalBottomSheet<String>(
      context: context,
      builder: (ctx) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          children: [
            const ListTile(
              title: Text('Reattach to existing shell?'),
              subtitle: Text('Or start a fresh one.'),
            ),
            const Divider(height: 1),
            ListTile(
              leading: const Icon(Icons.add_circle_outline),
              title: const Text('New shell'),
              onTap: () => Navigator.pop(ctx, ''),
            ),
            for (final p in reattachable)
              ListTile(
                leading: const Icon(Icons.history),
                title: Text(p.cwd.isEmpty ? p.command : p.cwd),
                subtitle:
                    Text('${p.command} · last seen ${_humanAgoUtc(p.lastSeenUnixMs)}'),
                onTap: () => Navigator.pop(ctx, p.handle),
              ),
          ],
        ),
      ),
    );
    if (!mounted) return;
    if (picked == null || picked.isEmpty) {
      setState(() => _tabs.add(TerminalTabModel(initialLabel: 'shell')));
    } else {
      setState(() {
        final tab = TerminalTabModel(initialLabel: 'reattaching…');
        tab.reattachHandle = picked;
        _tabs.add(tab);
      });
    }
  }

  @override
  void dispose() {
    _reconnectTimer?.cancel();
    _resumedBannerTimer?.cancel();
    _ptyExitWatcher?.cancel();
    final bridge = ref.read(bridgeProvider);
    for (final tab in _tabs) {
      final id = tab.ptyId;
      if (id != null) bridge.closePty(ptyId: id);
      tab.disposeModel();
    }
    _session?.close();
    super.dispose();
  }

  Future<void> _addTab() async {
    final session = _session;
    if (session == null) return;
    // Offer reattach if persisted PTYs exist and aren't already in
    // another open tab. Otherwise spawn a fresh shell directly.
    List<PtyHandleInfo> persisted = const [];
    try {
      persisted =
          await ref.read(bridgeProvider).listPtys(sessionId: session.id);
    } catch (_) {}
    if (!mounted) return;
    final boundHandles = _tabs.map((t) => t.reattachHandle).toSet();
    final candidates = persisted
        .where((p) => !p.attached && !boundHandles.contains(p.handle))
        .toList(growable: false);
    if (candidates.isEmpty) {
      _spawnNewTab();
      return;
    }
    final picked = await showModalBottomSheet<String>(
      context: context,
      builder: (ctx) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          children: [
            ListTile(
              leading: const Icon(Icons.add_circle_outline),
              title: const Text('New shell'),
              onTap: () => Navigator.pop(ctx, ''),
            ),
            const Divider(height: 1),
            for (final p in candidates)
              ListTile(
                leading: const Icon(Icons.history),
                title: Text(p.cwd.isEmpty ? p.command : p.cwd),
                subtitle:
                    Text('${p.command} · last seen ${_humanAgoUtc(p.lastSeenUnixMs)}'),
                onTap: () => Navigator.pop(ctx, p.handle),
              ),
          ],
        ),
      ),
    );
    if (!mounted) return;
    if (picked == null) return;
    if (picked.isEmpty) {
      _spawnNewTab();
    } else {
      setState(() {
        _tabSeq += 1;
        final tab = TerminalTabModel(initialLabel: 'reattaching…');
        tab.reattachHandle = picked;
        _tabs.add(tab);
        _activeIndex = _tabs.length - 1;
      });
    }
  }

  void _spawnNewTab() {
    setState(() {
      _tabSeq += 1;
      _tabs.add(TerminalTabModel(initialLabel: 'tab $_tabSeq'));
      _activeIndex = _tabs.length - 1;
    });
  }

  Future<void> _killTabPty(int idx) async {
    if (idx < 0 || idx >= _tabs.length) return;
    final tab = _tabs[idx];
    final handle = tab.reattachHandle;
    final session = _session;
    if (handle.isEmpty || session == null) {
      await _closeTab(idx);
      return;
    }
    try {
      await ref
          .read(bridgeProvider)
          .killPty(sessionId: session.id, handle: handle);
    } catch (_) {}
    await ref.read(persistedPtyHandlesProvider.notifier).forget(
          serverId: widget.server.id,
          handle: handle,
        );
    await _closeTab(idx);
  }

  Future<void> _closeTab(int idx) async {
    if (idx < 0 || idx >= _tabs.length) return;
    final removed = _tabs[idx];
    final id = removed.ptyId;
    if (id != null) {
      try {
        await ref.read(bridgeProvider).closePty(ptyId: id);
      } catch (_) {}
    }
    removed.disposeModel();
    if (!mounted) return;
    setState(() {
      _tabs.removeAt(idx);
      if (_tabs.isEmpty) {
        _activeIndex = 0;
        return;
      }
      if (_activeIndex >= _tabs.length) _activeIndex = _tabs.length - 1;
      if (_activeIndex < 0) _activeIndex = 0;
    });
  }

  Future<void> _sendBytes(List<int> data) async {
    if (_tabs.isEmpty) return;
    final tab = _tabs[_activeIndex];
    final id = tab.ptyId;
    if (id == null) return;
    await ref
        .read(bridgeProvider)
        .ptyInput(ptyId: id, data: Uint8List.fromList(data));
  }

  Future<void> _openIme() async {
    final result = await ImeInputSheet.show(context);
    if (result == null || !mounted) return;
    final normalized = terminalInputFromEditorText(result.text,
        appendEnter: result.appendEnter);
    await _sendBytes(utf8.encode(normalized));
  }

  Future<void> _openFileBrowser() async {
    if (_tabs.isEmpty) return;
    final id = _tabs[_activeIndex].ptyId;
    if (id == null) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => FileBrowserScreen(
          ptyId: id,
          title: '${widget.server.name} files',
        ),
      ),
    );
  }

  Future<void> _openTextViewer() async {
    final s = _session;
    if (s == null) return;
    final controller = TextEditingController();
    final path = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('View remote file'),
        content: TextField(
          controller: controller,
          decoration: const InputDecoration(
            labelText: 'Absolute path',
            hintText: r'C:\Windows\System32\drivers\etc\hosts',
          ),
          autofocus: true,
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, controller.text),
            child: const Text('Open'),
          ),
        ],
      ),
    );
    if (path == null || path.trim().isEmpty || !mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => TextViewerScreen(session: s, path: path.trim()),
      ),
    );
  }

  void _toggleWrap() {
    if (_tabs.isEmpty) return;
    final tab = _tabs[_activeIndex];
    final settingsDefault = ref.read(settingsProvider).maybeWhen<bool>(
          data: (s) => s.lineWrap,
          orElse: () => true,
        );
    final current = tab.lineWrapOverride ?? settingsDefault;
    setState(() {
      tab.lineWrapOverride = !current;
    });
  }

  @override
  Widget build(BuildContext context) {
    if (_connState == _ConnState.error) {
      return Scaffold(
        appBar: AppBar(title: Text(widget.server.name)),
        body: _ConnectError(
          message: _connectError ?? 'Connection failed.',
          onRetry: () {
            setState(() {
              _connectError = null;
              _session = null;
              _reconnectAttempts = 0;
              _connState = _ConnState.connecting;
            });
            _connectSession();
          },
        ),
      );
    }
    if (_connState == _ConnState.reconnecting && _session == null) {
      return Scaffold(
        appBar: AppBar(title: Text(widget.server.name)),
        body: _ReconnectingScreen(
          attempt: _reconnectAttempts,
          maxAttempts: _maxReconnectAttempts,
          lastError: _connectError,
          onCancel: _cancelReconnect,
        ),
      );
    }

    final session = _session;
    final settingsDefault = ref.watch(settingsProvider).maybeWhen<bool>(
          data: (s) => s.lineWrap,
          orElse: () => true,
        );
    final lineWrap = _tabs.isEmpty
        ? true
        : (_tabs[_activeIndex].lineWrapOverride ?? settingsDefault);

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.server.name),
        actions: [
          IconButton(
            tooltip: 'New tab',
            onPressed: session == null ? null : _addTab,
            icon: const Icon(Icons.add),
          ),
          IconButton(
            tooltip: lineWrap ? 'Switch to horizontal scroll' : 'Switch to wrap',
            onPressed: _tabs.isEmpty ? null : _toggleWrap,
            icon: Icon(lineWrap ? Icons.wrap_text : Icons.swap_horiz),
          ),
          IconButton(
            tooltip: 'Browse files',
            onPressed: _tabs.isEmpty ? null : _openFileBrowser,
            icon: const Icon(Icons.folder_open_outlined),
          ),
          IconButton(
            tooltip: 'View remote file',
            onPressed: session == null ? null : _openTextViewer,
            icon: const Icon(Icons.description_outlined),
          ),
        ],
      ),
      body: SafeArea(
        child: session == null
            ? const Center(child: CircularProgressIndicator())
            : Column(
                children: [
                  if (_showResumedBanner) const _ResumedBanner(),
                  _TabBar(
                    tabs: _tabs,
                    activeIndex: _activeIndex,
                    onTap: (i) => setState(() => _activeIndex = i),
                    onClose: _closeTab,
                    onKill: _killTabPty,
                  ),
                  Expanded(
                    child: _tabs.isEmpty
                        ? const _NoTabsState()
                        : IndexedStack(
                            index: _activeIndex.clamp(0, _tabs.length - 1),
                            children: [
                              for (final tab in _tabs)
                                TerminalPane(
                                  key: ValueKey(tab),
                                  session: session,
                                  tab: tab,
                                ),
                            ],
                          ),
                  ),
                  SpecialKeysBar(
                    onSendBytes: _sendBytes,
                    onImeInput: _openIme,
                  ),
                ],
              ),
      ),
    );
  }
}

class _ResumedBanner extends StatelessWidget {
  const _ResumedBanner();

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: double.infinity,
      color: scheme.primaryContainer,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      child: Row(
        children: [
          Icon(Icons.cable, color: scheme.onPrimaryContainer, size: 18),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              'Session resumed',
              style: TextStyle(
                color: scheme.onPrimaryContainer,
                fontWeight: FontWeight.w500,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _TabBar extends StatelessWidget {
  const _TabBar({
    required this.tabs,
    required this.activeIndex,
    required this.onTap,
    required this.onClose,
    required this.onKill,
  });

  final List<TerminalTabModel> tabs;
  final int activeIndex;
  final ValueChanged<int> onTap;
  final ValueChanged<int> onClose;
  final ValueChanged<int> onKill;

  @override
  Widget build(BuildContext context) {
    if (tabs.isEmpty) return const SizedBox.shrink();
    return Material(
      color: Theme.of(context).colorScheme.surfaceContainer,
      child: SizedBox(
        height: 40,
        child: ListView.builder(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.symmetric(horizontal: 4),
          itemCount: tabs.length,
          itemBuilder: (context, i) {
            final tab = tabs[i];
            return AnimatedBuilder(
              animation: tab,
              builder: (_, __) => _TabChip(
                label: tab.label,
                active: i == activeIndex,
                onTap: () => onTap(i),
                onClose: () => onClose(i),
                onLongPress: () async {
                  final action = await showModalBottomSheet<String>(
                    context: context,
                    builder: (ctx) => SafeArea(
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          ListTile(
                            leading: const Icon(Icons.close),
                            title: const Text('Close tab (keep PTY alive)'),
                            subtitle: const Text(
                                'Server keeps the shell for ~30 minutes.'),
                            onTap: () => Navigator.pop(ctx, 'close'),
                          ),
                          ListTile(
                            leading: const Icon(Icons.delete_forever),
                            title: const Text('Kill PTY'),
                            subtitle: const Text(
                                'Stops the child process and drops scrollback.'),
                            onTap: () => Navigator.pop(ctx, 'kill'),
                          ),
                        ],
                      ),
                    ),
                  );
                  if (action == 'close') onClose(i);
                  if (action == 'kill') onKill(i);
                },
              ),
            );
          },
        ),
      ),
    );
  }
}

class _TabChip extends StatelessWidget {
  const _TabChip({
    required this.label,
    required this.active,
    required this.onTap,
    required this.onClose,
    required this.onLongPress,
  });

  final String label;
  final bool active;
  final VoidCallback onTap;
  final VoidCallback onClose;
  final VoidCallback onLongPress;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final bg = active ? scheme.primaryContainer : Colors.transparent;
    final fg = active ? scheme.onPrimaryContainer : scheme.onSurface;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 4),
      child: InkWell(
        onTap: onTap,
        onLongPress: onLongPress,
        borderRadius: BorderRadius.circular(8),
        child: Container(
          decoration: BoxDecoration(
            color: bg,
            borderRadius: BorderRadius.circular(8),
            border: Border.all(color: scheme.outlineVariant),
          ),
          padding: const EdgeInsets.fromLTRB(10, 4, 4, 4),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                label,
                style: TextStyle(color: fg, fontFamily: 'monospace'),
                overflow: TextOverflow.ellipsis,
              ),
              const SizedBox(width: 4),
              IconButton(
                visualDensity: VisualDensity.compact,
                padding: EdgeInsets.zero,
                constraints: const BoxConstraints(minWidth: 24, minHeight: 24),
                onPressed: onClose,
                icon: Icon(Icons.close, size: 16, color: fg),
                tooltip: 'Close tab',
              ),
            ],
          ),
        ),
      ),
    );
  }
}

String _humanAgoUtc(int ms) {
  if (ms <= 0) return 'unknown';
  final delta = DateTime.now().millisecondsSinceEpoch - ms;
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (delta < minute) return 'just now';
  if (delta < hour) return '${(delta / minute).floor()}m ago';
  if (delta < day) return '${(delta / hour).floor()}h ago';
  return '${(delta / day).floor()}d ago';
}

class _NoTabsState extends StatelessWidget {
  const _NoTabsState();

  @override
  Widget build(BuildContext context) {
    return const Center(
      child: Padding(
        padding: EdgeInsets.all(24),
        child: Text(
          'No terminals.\nTap + to open one.',
          textAlign: TextAlign.center,
        ),
      ),
    );
  }
}

class _ConnectError extends StatelessWidget {
  const _ConnectError({required this.message, required this.onRetry});
  final String message;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 48),
            const SizedBox(height: 12),
            SelectableText(
              message,
              textAlign: TextAlign.center,
              style: const TextStyle(fontFamily: 'monospace'),
            ),
            const SizedBox(height: 16),
            FilledButton.icon(
              icon: const Icon(Icons.refresh),
              label: const Text('Retry'),
              onPressed: onRetry,
            ),
          ],
        ),
      ),
    );
  }
}

class _ReconnectingScreen extends StatelessWidget {
  const _ReconnectingScreen({
    required this.attempt,
    required this.maxAttempts,
    required this.onCancel,
    this.lastError,
  });

  final int attempt;
  final int maxAttempts;
  final String? lastError;
  final VoidCallback onCancel;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const SizedBox(
              width: 40,
              height: 40,
              child: CircularProgressIndicator(),
            ),
            const SizedBox(height: 16),
            Text(
              'Reconnecting…  ($attempt / $maxAttempts)',
              style: Theme.of(context).textTheme.titleMedium,
            ),
            if (lastError != null) ...[
              const SizedBox(height: 8),
              SelectableText(
                lastError!,
                textAlign: TextAlign.center,
                style: TextStyle(
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                  fontFamily: 'monospace',
                  fontSize: 12,
                ),
              ),
            ],
            const SizedBox(height: 16),
            OutlinedButton.icon(
              icon: const Icon(Icons.close),
              label: const Text('Stop trying'),
              onPressed: onCancel,
            ),
          ],
        ),
      ),
    );
  }
}
