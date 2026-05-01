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

import '../models/server_entry.dart';
import '../services/peersh_session.dart';
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
      setState(() {
        _session = session;
        if (_tabs.isEmpty) {
          _tabs.add(TerminalTabModel(initialLabel: 'shell'));
        }
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _connectError = '$e');
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

  void _addTab() {
    setState(() {
      _tabSeq += 1;
      _tabs.add(TerminalTabModel(initialLabel: 'tab $_tabSeq'));
      _activeIndex = _tabs.length - 1;
    });
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
  });

  final List<TerminalTabModel> tabs;
  final int activeIndex;
  final ValueChanged<int> onTap;
  final ValueChanged<int> onClose;

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
  });

  final String label;
  final bool active;
  final VoidCallback onTap;
  final VoidCallback onClose;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final bg = active ? scheme.primaryContainer : Colors.transparent;
    final fg = active ? scheme.onPrimaryContainer : scheme.onSurface;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 4),
      child: InkWell(
        onTap: onTap,
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
