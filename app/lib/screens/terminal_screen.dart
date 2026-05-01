// Phase 8 Tier 1 — interactive PTY terminal screen.
//
// Wraps an xterm.dart `Terminal` against a peersh PTY session. Two
// display modes coexist (per-screen toggle on top of the persisted
// settings default):
//
//   - Wrap: standard `TerminalView`, autoResize=true. The visible grid
//     follows the device viewport; xterm wraps lines at the right edge.
//     For PowerShell-flavoured shells the REMOTE PTY is sized to at
//     least 120 columns so PowerShell's formatter doesn't truncate
//     output for a 36-cell phone screen — see resize_policy.dart.
//
//   - Scroll: TerminalView with autoResize=false, sized to exactly 120
//     cells, wrapped in a horizontal SingleChildScrollView. The user
//     scrolls horizontally to see beyond the screen edge. Both the
//     local xterm and the remote PTY are pinned to 120 columns so
//     PowerShell's idea of "console width" matches what xterm renders.
//
// Connect order: render the TerminalView eagerly (with a translucent
// loading overlay while the QUIC session negotiates), pre-seed the host
// PTY using a viewport estimate so PSReadLine doesn't latch onto the
// xterm-default 80x24, then track xterm's onResize callbacks for any
// post-startup adjustments.

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:xterm/xterm.dart';

import '../models/pty_event.dart';
import '../models/server_entry.dart';
import '../services/peersh_session.dart';
import '../state/settings.dart';
import '../terminal/resize_policy.dart';
import '../terminal/viewport_estimate.dart';
import '../widgets/special_keys_bar.dart';
import 'file_browser_screen.dart';
import 'ime_input_sheet.dart';
import 'text_viewer_screen.dart';

class TerminalScreen extends ConsumerStatefulWidget {
  const TerminalScreen({super.key, required this.server});

  final ServerEntry server;

  @override
  ConsumerState<TerminalScreen> createState() => _TerminalScreenState();
}

class _TerminalScreenState extends ConsumerState<TerminalScreen>
    with WidgetsBindingObserver {
  PeershSession? _session;
  int? _ptyId;
  bool _openingPty = false;
  String? _connectError;

  /// Per-screen override on top of [settingsProvider]'s default.
  bool? _localLineWrapOverride;

  late final Terminal _terminal = Terminal(maxLines: 10000);
  late final TerminalController _ctrl = TerminalController();

  StreamSubscription<PtyEvent>? _ptySub;
  Timer? _resizeDebounce;

  /// Last cols/rows we successfully sent in a PTYResize message. Compared
  /// against the next request to suppress no-op chatter.
  int _sentCols = 0;
  int _sentRows = 0;

  /// Tracks the shell the host actually spawned. Today we always spawn
  /// pwsh (peershd's default), so `isPowerShellShell` is true; reserved
  /// for the future where PTYRequest can name a different binary.
  final String _shell = 'auto';

  // _utf8 is a streaming UTF-8 decoder. PTY chunks are arbitrary byte
  // boundaries — a single multi-byte glyph can span two PTYData frames —
  // so we must NOT call utf8.decode on each chunk independently. The
  // streaming converter holds the partial-byte state across chunks.
  final Utf8Decoder _utf8 = const Utf8Decoder(allowMalformed: true);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _terminal.onOutput = _handleTerminalOutput;
    _terminal.onResize = (cols, rows, _, __) => _onTerminalResize(cols, rows);
    _connectSession();
  }

  @override
  void didChangeMetrics() {
    // Orientation change, IME show/hide, foldable hinge — all of these
    // change the available area. Force a rebuild so the TerminalView
    // re-measures and emits a fresh onResize.
    if (mounted) setState(() {});
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _resizeDebounce?.cancel();
    _ptySub?.cancel();
    final id = _ptyId;
    final s = _session;
    if (id != null) {
      ref.read(bridgeProvider).closePty(ptyId: id);
    }
    if (s != null) {
      s.close();
    }
    _ctrl.dispose();
    super.dispose();
  }

  bool _resolvedLineWrap() {
    final override = _localLineWrapOverride;
    if (override != null) return override;
    return ref.read(settingsProvider).maybeWhen(
          data: (s) => s.lineWrap,
          orElse: () => true,
        );
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
      setState(() => _session = session);
      // Pre-seed PTY size from the viewport estimate so the shell's
      // startup banner doesn't latch onto xterm's default 80x24.
      _maybeOpenPty();
    } catch (e) {
      if (!mounted) return;
      setState(() => _connectError = '$e');
    }
  }

  Future<void> _maybeOpenPty() async {
    if (_openingPty || _ptyId != null) return;
    final session = _session;
    if (session == null) return;
    final fontSize = ref.read(settingsProvider).maybeWhen(
          data: (s) => s.fontSize,
          orElse: () => 13.0,
        );
    final dims = estimateViewportCells(context, fontSize: fontSize);
    final lineWrap = _resolvedLineWrap();
    final visibleCols = lineWrap ? dims.cols : kHorizontalScrollCols;
    final remoteCols = remoteColsFor(
      shell: _shell,
      lineWrap: lineWrap,
      visibleCols: visibleCols,
    );

    _openingPty = true;
    try {
      final ptyId = await ref.read(bridgeProvider).openPty(
            sessionId: session.id,
            cols: remoteCols,
            rows: dims.rows,
          );
      if (!mounted) {
        await ref.read(bridgeProvider).closePty(ptyId: ptyId);
        return;
      }
      _sentCols = remoteCols;
      _sentRows = dims.rows;
      _ptySub = ref.read(bridgeProvider).ptyEvents().listen(_onPtyEvent);
      setState(() => _ptyId = ptyId);
    } catch (e) {
      if (!mounted) return;
      setState(() => _connectError = '$e');
    } finally {
      _openingPty = false;
    }
  }

  /// Called by xterm whenever it computes a new grid size for itself.
  /// Translates into a debounced PTYResize against the host.
  void _onTerminalResize(int cols, int rows) {
    if (cols <= 0 || rows <= 0) return;
    if (_ptyId == null) {
      // PTY not opened yet; the initial size is set by _maybeOpenPty.
      return;
    }
    final lineWrap = _resolvedLineWrap();
    final visibleCols = lineWrap ? cols : kHorizontalScrollCols;
    final remoteCols = remoteColsFor(
      shell: _shell,
      lineWrap: lineWrap,
      visibleCols: visibleCols,
    );
    if (remoteCols == _sentCols && rows == _sentRows) return;
    _resizeDebounce?.cancel();
    _resizeDebounce = Timer(const Duration(milliseconds: 100), () {
      final id = _ptyId;
      if (id == null) return;
      _sentCols = remoteCols;
      _sentRows = rows;
      ref
          .read(bridgeProvider)
          .ptyResize(ptyId: id, cols: remoteCols, rows: rows)
          .catchError((_) {});
    });
  }

  /// Toggle wrap/scroll. Forces a fresh resize push since the remote-cols
  /// formula changes between modes.
  void _toggleWrap(bool wrap) {
    setState(() => _localLineWrapOverride = wrap);
    final id = _ptyId;
    if (id == null) return;
    final visibleCols = wrap ? _terminal.viewWidth : kHorizontalScrollCols;
    final remoteCols = remoteColsFor(
      shell: _shell,
      lineWrap: wrap,
      visibleCols: visibleCols,
    );
    final rows = _terminal.viewHeight == 0 ? 24 : _terminal.viewHeight;
    _sentCols = remoteCols;
    _sentRows = rows;
    ref
        .read(bridgeProvider)
        .ptyResize(ptyId: id, cols: remoteCols, rows: rows)
        .catchError((_) {});
  }

  void _onPtyEvent(PtyEvent event) {
    if (event.ptyId != _ptyId) return;
    if (event is PtyDataEvent) {
      _terminal.write(_utf8.convert(event.data));
    } else if (event is PtyExitEvent) {
      _terminal.write(
        '\r\n\x1b[33m[pty exited code=${event.exitCode}${event.error.isEmpty ? '' : ' err=${event.error}'}]\x1b[0m\r\n',
      );
    }
  }

  Future<void> _handleTerminalOutput(String text) async {
    final id = _ptyId;
    if (id == null) return;
    final bytes = Uint8List.fromList(utf8.encode(text));
    try {
      await ref.read(bridgeProvider).ptyInput(ptyId: id, data: bytes);
    } catch (_) {
      // Best-effort: drop keystroke if the platform side isn't ready.
    }
  }

  Future<void> _sendBytes(List<int> data) async {
    final id = _ptyId;
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

  Future<void> _openFileBrowser() async {
    final id = _ptyId;
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

  @override
  Widget build(BuildContext context) {
    final settingsAsync = ref.watch(settingsProvider);
    final fontSize = settingsAsync.maybeWhen(
      data: (s) => s.fontSize,
      orElse: () => 13.0,
    );
    final lineWrap = _resolvedLineWrap();

    if (_connectError != null) {
      return Scaffold(
        appBar: AppBar(title: Text(widget.server.name)),
        body: _ConnectError(
          message: _connectError!,
          onRetry: () {
            setState(() {
              _connectError = null;
              _ptyId = null;
              _session = null;
            });
            _connectSession();
          },
        ),
      );
    }

    final showLoader = _session == null || _ptyId == null;

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.server.name),
        actions: [
          IconButton(
            tooltip: lineWrap ? 'Switch to horizontal scroll' : 'Switch to wrap',
            onPressed: () => _toggleWrap(!lineWrap),
            icon: Icon(lineWrap ? Icons.wrap_text : Icons.swap_horiz),
          ),
          IconButton(
            tooltip: 'Browse files (cwd-scoped)',
            onPressed: _ptyId == null ? null : _openFileBrowser,
            icon: const Icon(Icons.folder_open_outlined),
          ),
          IconButton(
            tooltip: 'View remote file',
            onPressed: _session == null ? null : _openTextViewer,
            icon: const Icon(Icons.description_outlined),
          ),
        ],
      ),
      body: SafeArea(
        child: Stack(
          children: [
            Column(
              children: [
                Expanded(
                  child: lineWrap
                      ? _WrapBody(terminal: _terminal, ctrl: _ctrl, fontSize: fontSize)
                      : _ScrollBody(terminal: _terminal, ctrl: _ctrl, fontSize: fontSize),
                ),
                SpecialKeysBar(
                  onSendBytes: _sendBytes,
                  onImeInput: _openIme,
                ),
              ],
            ),
            if (showLoader)
              Positioned.fill(
                child: ColoredBox(
                  color: Colors.black.withOpacity(0.55),
                  child: const Center(child: CircularProgressIndicator()),
                ),
              ),
          ],
        ),
      ),
    );
  }
}

/// Wrap mode: standard TerminalView. xterm autoResizes the grid to the
/// viewport and wraps long lines at the visible right edge.
class _WrapBody extends StatelessWidget {
  const _WrapBody({
    required this.terminal,
    required this.ctrl,
    required this.fontSize,
  });

  final Terminal terminal;
  final TerminalController ctrl;
  final double fontSize;

  @override
  Widget build(BuildContext context) {
    return TerminalView(
      terminal,
      controller: ctrl,
      autofocus: true,
      textStyle: TerminalStyle(fontSize: fontSize),
      backgroundOpacity: 1.0,
      padding: const EdgeInsets.all(4),
    );
  }
}

/// Scroll mode: pin xterm at exactly [kHorizontalScrollCols] columns,
/// disable autoResize, and wrap in a horizontal SingleChildScrollView so
/// the user can pan side-to-side. Both local xterm and remote PTY use
/// the same column count, so the shell's formatter and the rendered
/// width agree.
class _ScrollBody extends StatelessWidget {
  const _ScrollBody({
    required this.terminal,
    required this.ctrl,
    required this.fontSize,
  });

  final Terminal terminal;
  final TerminalController ctrl;
  final double fontSize;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, c) {
        // Generous over-estimate so xterm's true cell width never
        // exceeds the SizedBox and clips the rightmost cells.
        final cellW = fontSize * 0.7;
        final cellH = fontSize * 1.2;
        final width = kHorizontalScrollCols * cellW;
        final rows = (c.maxHeight / cellH).floor().clamp(5, 200);
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (terminal.viewWidth != kHorizontalScrollCols ||
              terminal.viewHeight != rows) {
            terminal.resize(kHorizontalScrollCols, rows);
          }
        });
        return SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          child: SizedBox(
            width: width,
            height: c.maxHeight,
            child: TerminalView(
              terminal,
              controller: ctrl,
              autoResize: false,
              autofocus: true,
              textStyle: TerminalStyle(fontSize: fontSize),
              backgroundOpacity: 1.0,
              padding: const EdgeInsets.all(4),
            ),
          ),
        );
      },
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
