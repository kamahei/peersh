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
import '../services/mobile_device_registry.dart';
import '../services/peersh_session.dart';
import '../state/persisted_notify_config.dart';
import '../state/persisted_pty_handles.dart';
import '../state/settings.dart';
import '../terminal/resize_policy.dart';
import '../terminal/viewport_estimate.dart';

/// Mutable per-tab state held by the parent screen so the tab survives
/// when the user switches away. Created when the tab is added; the
/// [TerminalPane] widget reads / mutates these fields directly.
class TerminalTabModel extends ChangeNotifier {
  TerminalTabModel({String initialLabel = 'shell'}) : _label = initialLabel;

  // reflowEnabled: false — TUI apps (claude / codex / vim) use the
  // alt-screen with absolute cursor positioning. xterm.dart's reflow
  // rewrites prior lines on width change (rotation, IME, resize), which
  // shreds those buffers. Disable it; let the host re-render via the
  // SIGWINCH-driven repaint instead.
  final Terminal terminal = Terminal(maxLines: 10000, reflowEnabled: false);
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
  String get lastCwd => _lastCwd;

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
  // Stateful UTF-8 decoder. PTY chunks split multi-byte sequences (box
  // drawing, emoji, Japanese) at arbitrary byte boundaries; decoding
  // each chunk independently with `convert` would replace both halves
  // with U+FFFD and corrupt TUI output from claude / codex.
  late final _Utf8StreamDecoder _utf8 = _Utf8StreamDecoder();
  // Holds events that arrive on the broadcast stream before the local
  // ptyId has been assigned (i.e. before bridge.openPty's
  // MethodChannel result has been delivered to Dart). On reattach the
  // host emits PTYData(replay) immediately after ReattachAck, and
  // because both the EventChannel send and the MethodChannel result
  // are FIFO-posted to the platform main handler, the replay event
  // races ahead of the openPty result. Without this buffer the replay
  // bytes are filtered out by the `id == null` guard in _onEvent.
  final List<PtyEvent> _preOpenBuffer = <PtyEvent>[];
  static const int _preOpenBufferLimit = 256;

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
      _sub?.cancel();
      _sub = ref.read(bridgeProvider).ptyEvents().listen(_onEvent);
      _scheduleCwdRefresh();
    }
  }

  @override
  void didUpdateWidget(TerminalPane oldWidget) {
    super.didUpdateWidget(oldWidget);
    // Live QUIC reconnect: the parent screen rebuilds us with a fresh
    // PeershSession, and _probeOrReconnect has already cleared
    // tab.ptyId so we can rebind. The reattach handle is preserved on
    // the tab, and openPty will present it to the host.
    //
    // Clear the local xterm buffer first so the host's replay (the
    // ring-buffer snapshot) doesn't double up on top of the same
    // content that's already visible.
    if (oldWidget.session.id != widget.session.id &&
        widget.tab.ptyId == null) {
      _sub?.cancel();
      _sub = null;
      _cwdRefresh?.cancel();
      _cwdRefresh = null;
      _lastSentCols = 0;
      _lastSentRows = 0;
      // Drop any pre-open events that may have lingered from the old
      // session — they reference a now-stale ptyId.
      _preOpenBuffer.clear();
      widget.tab.terminal.buffer.clear();
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _maybeOpenPty();
      });
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

  int _resolveTerminalCols() => ref.read(settingsProvider).maybeWhen(
        data: (s) => s.terminalCols,
        orElse: () => AppSettings.defaultTerminalCols,
      );

  Future<void> _maybeOpenPty() async {
    if (_opening || widget.tab.ptyId != null) return;
    _opening = true;
    final fontSize = ref.read(settingsProvider).maybeWhen(
          data: (s) => s.fontSize,
          orElse: () => 13.0,
        );
    final dims = estimateViewportCells(context, fontSize: fontSize);
    final lineWrap = _resolveLineWrap();
    final terminalCols = _resolveTerminalCols();
    final visibleCols = lineWrap ? dims.cols : terminalCols;
    final remoteCols = remoteColsFor(
      shell: 'auto',
      lineWrap: lineWrap,
      visibleCols: visibleCols,
      terminalCols: terminalCols,
    );
    // Subscribe BEFORE awaiting bridge.openPty so the EventChannel
    // sink is established (via onListen) and the broadcast stream
    // subscription exists before the host's replay PTYData frame is
    // emitted. Otherwise both the sink-null case and the
    // late-subscriber case silently drop the replay.
    _sub?.cancel();
    _sub = ref.read(bridgeProvider).ptyEvents().listen(_onEvent);
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
      // Drain any events that arrived on the broadcast stream before
      // ptyId was known. Replay bytes from the host land here when
      // the EventChannel post wins the race against the openPty
      // MethodChannel reply.
      if (_preOpenBuffer.isNotEmpty) {
        final pending = List<PtyEvent>.from(_preOpenBuffer);
        _preOpenBuffer.clear();
        for (final e in pending) {
          if (e.ptyId == result.ptyId) _dispatchEvent(e);
        }
      }
      // Reattach: the replay's trailing cursor sits at whatever
      // (col, row) the host's wide view had. Local viewports
      // (e.g. phone width 40 cols) clamp those coordinates and the
      // cursor visually lands on the wrong row. Send a bare CR so
      // the shell sees an empty submit and emits a fresh prompt on
      // a new line; cursor re-anchors to a known position while the
      // replay content stays visible above. Ctrl-L would clear the
      // scrollback under PowerShell's PSReadLine, which would defeat
      // the purpose of the replay. Reconnect time is the safe moment
      // for this — the user can't be mid-typing because the
      // connection was down.
      if (reattach.isNotEmpty) {
        unawaited(ref.read(bridgeProvider).ptyInput(
              ptyId: result.ptyId,
              data: Uint8List.fromList(const [0x0d]),
            ));
      }
      // Remember this handle locally so the user can rejoin later if
      // the connection drops or they leave + return.
      if (result.handle.isNotEmpty) {
        ref.read(persistedPtyHandlesProvider.notifier).remember(
              serverId: widget.session.serverId,
              handle: result.handle,
            );
      }
      await _restoreNotifyConfig(result.ptyId, result.handle);
      setState(() {});
      _scheduleCwdRefresh();
    } catch (e) {
      if (!mounted) return;
      _preOpenBuffer.clear();
      widget.tab.terminal
          .write('\r\n\x1b[31mfailed to open PTY: $e\x1b[0m\r\n');
    } finally {
      _opening = false;
    }
  }

  /// Apply persisted v2-B notify settings (or app-wide defaults) to this
  /// tab right after the PTY is opened. When a previously enabled tab
  /// reattaches, also push the config back to the host so notifications
  /// resume without the user having to re-toggle the bell.
  Future<void> _restoreNotifyConfig(int ptyId, String handle) async {
    final settings = ref.read(settingsProvider).value;
    final defaultThreshold = settings?.defaultNotifyThresholdSec ?? 10;
    final defaultIdle = settings?.defaultNotifyIdleSec ?? 0;

    NotifyConfig? persisted;
    if (handle.isNotEmpty) {
      try {
        final cfgs =
            await ref.read(persistedNotifyConfigsProvider.future);
        persisted = cfgs.lookup(widget.session.serverId, handle);
      } catch (e) {
        debugPrint('peersh: notify config lookup failed: $e');
      }
    }

    final thresholdSec = persisted?.thresholdSec ?? defaultThreshold;
    final idleSec = persisted?.idleSec ?? defaultIdle;
    final enabled = persisted?.enabled ?? false;

    widget.tab.notifyThreshold = Duration(seconds: thresholdSec);
    widget.tab.notifyIdleWindow = Duration(seconds: idleSec);
    widget.tab.notifyOnPromptReady = enabled;

    if (!enabled) return;
    try {
      final mobileId = await readOrCreateMobileDeviceId();
      await ref.read(bridgeProvider).ptyNotificationConfig(
            ptyId: ptyId,
            enabled: true,
            thresholdSeconds: thresholdSec,
            idleSeconds: idleSec,
            tabLabel: widget.tab.label,
            mobileDeviceId: mobileId,
          );
    } catch (e) {
      debugPrint('peersh: ptyNotificationConfig restore failed: $e');
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
    final terminalCols = _resolveTerminalCols();
    final visibleCols = lineWrap ? cols : terminalCols;
    final remoteCols = remoteColsFor(
      shell: 'auto',
      lineWrap: lineWrap,
      visibleCols: visibleCols,
      terminalCols: terminalCols,
    );
    // Both cols AND rows must be tracked: full-screen TUI apps (claude,
    // codex, vim) read rows to size their alt-screen layout. Phone
    // rotation and IME popup change rows without cols, and dropping
    // those leaves the host PTY with stale dimensions, causing the alt
    // screen to overflow off-screen or clip.
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
    if (id == null) {
      // ptyId not yet assigned — could be the host's replay frame
      // racing the openPty MethodChannel reply. Buffer; _maybeOpenPty
      // drains this list once the assigned ptyId is known.
      _preOpenBuffer.add(event);
      while (_preOpenBuffer.length > _preOpenBufferLimit) {
        _preOpenBuffer.removeAt(0);
      }
      return;
    }
    if (event.ptyId != id) return;
    _dispatchEvent(event);
  }

  void _dispatchEvent(PtyEvent event) {
    if (event is PtyDataEvent) {
      widget.tab.terminal.write(_utf8.add(event.data));
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
    _preOpenBuffer.clear();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final settings = ref.watch(settingsProvider).value;
    final fontSize = settings?.fontSize ?? 13.0;
    final terminalCols =
        settings?.terminalCols ?? AppSettings.defaultTerminalCols;
    final lineWrap = _resolveLineWrap();
    final showLoader = widget.tab.ptyId == null;
    return Stack(
      children: [
        lineWrap
            ? _WrapBody(tab: widget.tab, fontSize: fontSize)
            : _ScrollBody(
                tab: widget.tab,
                fontSize: fontSize,
                cols: terminalCols,
              ),
        if (showLoader)
          Positioned.fill(
            child: ColoredBox(
              color: Colors.black.withValues(alpha: 0.55),
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
  const _ScrollBody({
    required this.tab,
    required this.fontSize,
    required this.cols,
  });
  final TerminalTabModel tab;
  final double fontSize;
  final int cols;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, c) {
        final cellW = fontSize * 0.7;
        final cellH = fontSize * 1.2;
        final width = cols * cellW;
        final rows = (c.maxHeight / cellH).floor().clamp(5, 200);
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (tab.terminal.viewWidth != cols ||
              tab.terminal.viewHeight != rows) {
            tab.terminal.resize(cols, rows);
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

/// Streaming UTF-8 decoder. Wraps xterm-bound input so multi-byte
/// sequences split across PTY chunks are reassembled instead of being
/// replaced with U+FFFD on each side of the boundary.
class _Utf8StreamDecoder {
  final StringBuffer _out = StringBuffer();
  late final ByteConversionSink _sink =
      const Utf8Decoder(allowMalformed: true)
          .startChunkedConversion(StringConversionSink.fromStringSink(_out));

  String add(List<int> bytes) {
    if (bytes.isEmpty) return '';
    _sink.add(bytes);
    final s = _out.toString();
    _out.clear();
    return s;
  }
}
