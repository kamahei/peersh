// Phase 6b Tier 1 — multi-tab terminal screen.
//
// Hosts a list of TerminalTabModel/TerminalPane pairs against ONE
// PeershSession (one QUIC connection). Each tab owns its own xterm
// Terminal + PTY session id; the panes are kept alive via IndexedStack
// so a backgrounded tab keeps streaming output into its scrollback.
//
// PTY reattach across reconnects (and matching server-side persistence
// + scrollback ring buffer) is Tier 2 of Phase 6b.

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/pty_file.dart';
import '../models/server_entry.dart';
import '../services/peersh_session.dart';
import '../state/persisted_pty_handles.dart';
import '../state/settings.dart';
import '../widgets/special_keys_bar.dart';
import 'file_browser_screen.dart';
import 'ime_input_sheet.dart';
import 'terminal_pane.dart';
import 'text_viewer_screen.dart';

class TerminalTabsScreen extends ConsumerStatefulWidget {
  const TerminalTabsScreen({super.key, required this.server});

  final ServerEntry server;

  @override
  ConsumerState<TerminalTabsScreen> createState() =>
      _TerminalTabsScreenState();
}

class _TerminalTabsScreenState extends ConsumerState<TerminalTabsScreen> {
  PeershSession? _session;
  String? _connectError;
  final List<TerminalTabModel> _tabs = [];
  int _activeIndex = 0;
  int _tabSeq = 1;

  @override
  void initState() {
    super.initState();
    _connectSession();
  }

  Future<void> _connectSession() async {
    final bridge = ref.read(bridgeProvider);
    try {
      final session = await PeershSession.open(
        bridge: bridge,
        server: widget.server,
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
        if (_tabs.isNotEmpty) return;
        if (persisted.where((p) => !p.attached).isEmpty) {
          // No reattachable PTYs — auto-spawn a fresh shell.
          _tabs.add(TerminalTabModel(initialLabel: 'shell'));
        }
        // Otherwise leave _tabs empty so the user picks via the prompt.
      });
      if (mounted && _tabs.isEmpty) {
        await _maybeOfferReattach(persisted);
      }
    } catch (e) {
      if (!mounted) return;
      setState(() => _connectError = '$e');
    }
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
    if (_connectError != null) {
      return Scaffold(
        appBar: AppBar(title: Text(widget.server.name)),
        body: _ConnectError(
          message: _connectError!,
          onRetry: () {
            setState(() {
              _connectError = null;
              _session = null;
            });
            _connectSession();
          },
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
