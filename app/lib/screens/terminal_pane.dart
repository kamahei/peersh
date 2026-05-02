// One terminal pane — a single xterm.dart Terminal bound to a single PTY
// session. Owned by TerminalTabsScreen (one pane per tab); kept alive
// across tab switches by IndexedStack so output keeps streaming into
// the offscreen tabs' scrollback.

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:xterm/xterm.dart';

import '../models/pty_event.dart';
import '../services/peersh_session.dart';
import '../state/settings.dart';
import '../state/persisted_pty_handles.dart';
import '../terminal/resize_policy.dart';
import '../terminal/viewport_estimate.dart';

/// Mutable per-tab state held by the parent screen so the tab survives
/// when the user switches away. Created when the tab is added; the
/// [TerminalPane] widget reads / mutates these fields directly.
class TerminalTabModel extends ChangeNotifier {
  TerminalTabModel({String initialLabel = 'shell'}) : _label = initialLabel;

  final Terminal terminal = Terminal(maxLines: 10000);
  final TerminalController controller = TerminalController();

  String _label;
  String get label => _label;

  /// Host-assigned PTY id, set after openPty completes.
  int? ptyId;

  /// Server-assigned reattach handle. Persisted across reconnects so
  /// the client can rebind to the same shell + scrollback.
  String reattachHandle = '';

  /// Last cwd reported by the host (for tab title).
  String _lastCwd = '';

  /// Track wrap mode locally so each tab can override the global default.
  bool? _localLineWrapOverride;

  /// v2-B push notification toggle. When true, the host sends an FCM
  /// notification to this device after the next prompt OSC 9;9
  /// occurrence (or output silence) past the threshold.
  bool _notifyOnPromptReady = false;
  bool get notifyOnPromptReady => _notifyOnPromptReady;
  set notifyOnPromptReady(bool v) {
    if (_notifyOnPromptReady == v) return;
    _notifyOnPromptReady = v;
    notifyListeners();
  }

  /// Minimum command duration before a prompt-ready notification fires.
  /// Default 10s — overridable per tab via the long-press dialog.
  Duration notifyThreshold = const Duration(seconds: 10);

  /// Output-silence window for the idle heuristic (Claude / Codex).
  /// 0 disables. Default 0 — opt in per tab.
  Duration notifyIdleWindow = Duration.zero;

  void setLabel(String v) {
    if (v.isEmpty) return;
    if (_label == v) return;
    _label = v;
    notifyListeners();
  }

  bool? get lineWrapOverride => _localLineWrapOverride;
  set lineWrapOverride(bool? v) {
    _localLineWrapOverride = v;
    notifyListeners();
  }

  /// Update tab label from a freshly-observed cwd. Returns true if the
  /// label changed (caller may want to repaint its tab bar).
  bool updateFromCwd(String cwd) {
    if (cwd.isEmpty || cwd == _lastCwd) return false;
    _lastCwd = cwd;
    final segments = cwd.split(RegExp(r'[\\/]'))..removeWhere((s) => s.isEmpty);
    if (segments.isNotEmpty) {
      setLabel(segments.last);
      return true;
    }
    return false;
  }

  void disposeModel() {
    controller.dispose();
  }
}

class TerminalPane extends ConsumerStatefulWidget {
  const TerminalPane({
    super.key,
    required this.session,
    required this.tab,
  });

  final PeershSession session;
  final TerminalTabModel tab;

  @override
  ConsumerState<TerminalPane> createState() => _TerminalPaneState();
}

class _TerminalPaneState extends ConsumerState<TerminalPane> {
  StreamSubscription<PtyEvent>? _sub;
  Timer? _resizeDebounce;
  Timer? _cwdRefresh;
  bool _opening = false;
  int _lastSentCols = 0;
  int _lastSentRows = 0;
  final Utf8Decoder _utf8 = const Utf8Decoder(allowMalformed: true);

  @override
  void initState() {
    super.initState();
    widget.tab.terminal.onOutput = _handleOutput;
    widget.tab.terminal.onResize = (cols, rows, _, __) => _onResize(cols, rows);
    if (widget.tab.ptyId == null) {
      // _maybeOpenPty reads MediaQuery, which is illegal during
      // initState. Defer to the first post-frame so the widget is
      // mounted in the tree and inherited widgets are reachable.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _maybeOpenPty();
      });
    } else {
      // Tab was already initialised previously (e.g. user switched away
      // and back). Re-attach the event listener; PTY id is unchanged.
      _sub = ref.read(bridgeProvider).ptyEvents().listen(_onEvent);
      _scheduleCwdRefresh();
    }
  }

  bool _resolveLineWrap() {
    final override = widget.tab.lineWrapOverride;
    if (override != null) return override;
    return ref.read(settingsProvider).maybeWhen(
          data: (s) => s.lineWrap,
          orElse: () => true,
        );
  }

  Future<void> _maybeOpenPty() async {
    if (_opening || widget.tab.ptyId != null) return;
    _opening = true;
    final fontSize = ref.read(settingsProvider).maybeWhen(
          data: (s) => s.fontSize,
          orElse: () => 13.0,
        );
    final dims = estimateViewportCells(context, fontSize: fontSize);
    final lineWrap = _resolveLineWrap();
    final visibleCols = lineWrap ? dims.cols : kHorizontalScrollCols;
    final remoteCols = remoteColsFor(
      shell: 'auto',
      lineWrap: lineWrap,
      visibleCols: visibleCols,
    );
    try {
      final reattach = widget.tab.reattachHandle;
      final result = await ref.read(bridgeProvider).openPty(
            sessionId: widget.session.id,
            cols: remoteCols,
            rows: dims.rows,
            reattachHandle: reattach,
          );
      if (!mounted) {
        await ref.read(bridgeProvider).closePty(ptyId: result.ptyId);
        return;
      }
      widget.tab.ptyId = result.ptyId;
      widget.tab.reattachHandle = result.handle;
      _lastSentCols = remoteCols;
      _lastSentRows = dims.rows;
      _sub = ref.read(bridgeProvider).ptyEvents().listen(_onEvent);
      // Remember this handle locally so the user can rejoin later if
      // the connection drops or they leave + return.
      if (result.handle.isNotEmpty) {
        ref.read(persistedPtyHandlesProvider.notifier).remember(
              serverId: widget.session.serverId,
              handle: result.handle,
            );
      }
      setState(() {});
      _scheduleCwdRefresh();
    } catch (e) {
      if (!mounted) return;
      widget.tab.terminal
          .write('\r\n\x1b[31mfailed to open PTY: $e\x1b[0m\r\n');
    } finally {
      _opening = false;
    }
  }

  void _scheduleCwdRefresh() {
    _cwdRefresh?.cancel();
    _cwdRefresh = Timer.periodic(const Duration(seconds: 2), (_) async {
      if (!mounted) return;
      final id = widget.tab.ptyId;
      if (id == null) return;
      try {
        final cwd = await ref.read(bridgeProvider).getCwd(ptyId: id);
        widget.tab.updateFromCwd(cwd);
      } catch (_) {
        // best-effort
      }
    });
  }

  Future<void> _handleOutput(String text) async {
    final id = widget.tab.ptyId;
    if (id == null) return;
    final bytes = Uint8List.fromList(utf8.encode(text));
    try {
      await ref.read(bridgeProvider).ptyInput(ptyId: id, data: bytes);
    } catch (_) {
      // best-effort
    }
  }

  void _onResize(int cols, int rows) {
    if (cols <= 0 || rows <= 0) return;
    final id = widget.tab.ptyId;
    if (id == null) return;
    final lineWrap = _resolveLineWrap();
    final visibleCols = lineWrap ? cols : kHorizontalScrollCols;
    final remoteCols = remoteColsFor(
      shell: 'auto',
      lineWrap: lineWrap,
      visibleCols: visibleCols,
    );
    if (remoteCols == _lastSentCols && rows == _lastSentRows) return;
    _resizeDebounce?.cancel();
    _resizeDebounce = Timer(const Duration(milliseconds: 100), () async {
      _lastSentCols = remoteCols;
      _lastSentRows = rows;
      try {
        await ref.read(bridgeProvider).ptyResize(
              ptyId: id,
              cols: remoteCols,
              rows: rows,
            );
      } catch (_) {}
    });
  }

  void _onEvent(PtyEvent event) {
    final id = widget.tab.ptyId;
    if (id == null || event.ptyId != id) return;
    if (event is PtyDataEvent) {
      widget.tab.terminal.write(_utf8.convert(event.data));
    } else if (event is PtyExitEvent) {
      widget.tab.terminal.write(
        '\r\n\x1b[33m[pty exited code=${event.exitCode}${event.error.isEmpty ? '' : ' err=${event.error}'}]\x1b[0m\r\n',
      );
    }
  }

  @override
  void dispose() {
    _resizeDebounce?.cancel();
    _cwdRefresh?.cancel();
    _sub?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final fontSize = ref.watch(settingsProvider).maybeWhen(
          data: (s) => s.fontSize,
          orElse: () => 13.0,
        );
    final lineWrap = _resolveLineWrap();
    final showLoader = widget.tab.ptyId == null;
    return Stack(
      children: [
        lineWrap
            ? _WrapBody(tab: widget.tab, fontSize: fontSize)
            : _ScrollBody(tab: widget.tab, fontSize: fontSize),
        if (showLoader)
          Positioned.fill(
            child: ColoredBox(
              color: Colors.black.withOpacity(0.55),
              child: const Center(child: CircularProgressIndicator()),
            ),
          ),
      ],
    );
  }
}

class _WrapBody extends StatelessWidget {
  const _WrapBody({required this.tab, required this.fontSize});
  final TerminalTabModel tab;
  final double fontSize;

  @override
  Widget build(BuildContext context) {
    return TerminalView(
      tab.terminal,
      controller: tab.controller,
      autofocus: true,
      textStyle: TerminalStyle(fontSize: fontSize),
      backgroundOpacity: 1.0,
      padding: const EdgeInsets.all(4),
    );
  }
}

class _ScrollBody extends StatelessWidget {
  const _ScrollBody({required this.tab, required this.fontSize});
  final TerminalTabModel tab;
  final double fontSize;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, c) {
        final cellW = fontSize * 0.7;
        final cellH = fontSize * 1.2;
        final width = kHorizontalScrollCols * cellW;
        final rows = (c.maxHeight / cellH).floor().clamp(5, 200);
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (tab.terminal.viewWidth != kHorizontalScrollCols ||
              tab.terminal.viewHeight != rows) {
            tab.terminal.resize(kHorizontalScrollCols, rows);
          }
        });
        return SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          child: SizedBox(
            width: width,
            height: c.maxHeight,
            child: TerminalView(
              tab.terminal,
              controller: tab.controller,
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
