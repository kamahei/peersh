import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/server_entry.dart';
import '../services/peersh_session.dart';
import '../state/settings.dart';
import '../widgets/log_view.dart';
import 'ime_input_sheet.dart';
import 'text_viewer_screen.dart';

/// Active terminal session screen. Handles the wrap/scroll toggle,
/// command input, IME bottom sheet, and the entry point to the text
/// viewer.
class TerminalScreen extends ConsumerStatefulWidget {
  const TerminalScreen({super.key, required this.server});

  final ServerEntry server;

  @override
  ConsumerState<TerminalScreen> createState() => _TerminalScreenState();
}

class _TerminalScreenState extends ConsumerState<TerminalScreen> {
  final _cmdCtrl = TextEditingController();
  PeershSession? _session;
  String? _connectError;
  bool _running = false;
  bool? _localLineWrapOverride;

  @override
  void initState() {
    super.initState();
    _connect();
  }

  Future<void> _connect() async {
    try {
      final s = await PeershSession.open(
        bridge: ref.read(bridgeProvider),
        server: widget.server,
      );
      if (!mounted) {
        s.close();
        return;
      }
      setState(() => _session = s);
      // Listen for events to repaint as output streams in.
      s.events.listen((_) {
        if (mounted) setState(() {});
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _connectError = '$e');
    }
  }

  @override
  void dispose() {
    _cmdCtrl.dispose();
    _session?.close();
    super.dispose();
  }

  Future<void> _runOnce() async {
    final s = _session;
    if (s == null) return;
    final cmd = _cmdCtrl.text.trim();
    if (cmd.isEmpty) return;
    setState(() => _running = true);
    try {
      await s.exec(cmd);
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('exec: $e')),
      );
    } finally {
      if (mounted) {
        setState(() {
          _running = false;
          _cmdCtrl.clear();
        });
      }
    }
  }

  Future<void> _openIme() async {
    final s = _session;
    if (s == null) return;
    final result = await ImeInputSheet.show(context);
    if (result == null || !mounted) return;
    final normalized = terminalInputFromEditorText(result.text,
        appendEnter: result.appendEnter);
    // For Phase 4b's simple log view we treat IME input as one Exec
    // (the host runs it as a single PowerShell statement). When Phase 6
    // introduces a true PTY, this becomes a write to the live shell.
    setState(() => _running = true);
    try {
      await s.exec(normalized.trimRight());
    } finally {
      if (mounted) setState(() => _running = false);
    }
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

  @override
  Widget build(BuildContext context) {
    final settingsAsync = ref.watch(settingsProvider);
    final defaultWrap = settingsAsync.maybeWhen(
      data: (s) => s.lineWrap,
      orElse: () => true,
    );
    final fontSize = settingsAsync.maybeWhen(
      data: (s) => s.fontSize,
      orElse: () => 13.0,
    );
    final wrap = _localLineWrapOverride ?? defaultWrap;

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.server.name),
        actions: [
          IconButton(
            tooltip: wrap ? 'Switch to scroll mode' : 'Switch to wrap mode',
            onPressed: () => setState(() => _localLineWrapOverride = !wrap),
            icon: Icon(wrap ? Icons.wrap_text : Icons.swap_horiz),
          ),
          IconButton(
            tooltip: 'View remote file',
            onPressed: _session == null ? null : _openTextViewer,
            icon: const Icon(Icons.description_outlined),
          ),
          IconButton(
            tooltip: 'IME input',
            onPressed: _session == null ? null : _openIme,
            icon: const Icon(Icons.keyboard_alt_outlined),
          ),
        ],
      ),
      body: _connectError != null
          ? _ConnectError(message: _connectError!, onRetry: () {
              setState(() {
                _connectError = null;
                _session = null;
              });
              _connect();
            })
          : _session == null
              ? const Center(child: CircularProgressIndicator())
              : Column(
                  children: [
                    Expanded(
                      child: LogView(
                        lines: _session!.bufferedLines,
                        partial: _session!.currentPartial,
                        lineWrap: wrap,
                        fontSize: fontSize,
                        errorMessage: _session!.completionError,
                      ),
                    ),
                    SafeArea(
                      top: false,
                      child: Padding(
                        padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
                        child: Row(
                          children: [
                            Expanded(
                              child: TextField(
                                controller: _cmdCtrl,
                                enabled: !_running,
                                decoration: const InputDecoration(
                                  isDense: true,
                                  border: OutlineInputBorder(),
                                  labelText: 'PowerShell command',
                                ),
                                onSubmitted: (_) => _runOnce(),
                              ),
                            ),
                            const SizedBox(width: 8),
                            FilledButton(
                              onPressed: _running ? null : _runOnce,
                              child: Text(_running ? '…' : 'Run'),
                            ),
                          ],
                        ),
                      ),
                    ),
                  ],
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
