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

import '../bridge.dart';
import '../models/pty_event.dart';
import '../models/pty_file.dart';
import '../models/server_entry.dart';
import '../services/flavor.dart' as flavor;
import '../services/mobile_device_registry.dart';
import '../services/peersh_session.dart';
import '../state/persisted_bg_keepalive.dart';
import '../state/persisted_idle_timeout.dart';
import '../state/persisted_notify_config.dart';
import '../state/persisted_pty_handles.dart';
import '../state/persisted_tabs.dart';
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
  const TerminalTabsScreen({
    super.key,
    required this.server,
    this.pendingTabLabel = '',
  });

  final ServerEntry server;

  /// Tab label that arrived via FCM tap deep-link. Once the session is
  /// established and tabs are populated, the screen tries to focus the
  /// tab whose label matches; empty string (the default) means "no
  /// preference, leave the active tab as-is".
  final String pendingTabLabel;

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
  bool _showNotifHint = false;
  static const _maxReconnectAttempts = 6;
  static const _baseBackoff = Duration(milliseconds: 500);
  static const _maxBackoff = Duration(seconds: 30);

  // Fullscreen toggle hides the AppBar, tab strip, and special-key bar
  // so the terminal viewport gets the entire screen. A small bottom
  // exit-bar stays visible so the user can leave fullscreen without
  // gestures (and without blowing past the system back button which
  // still pops the route). Mirrors httpssh's `_fullscreen` field.
  bool _fullscreen = false;

  // Tab snapshot persistence. Coalesces bursts of tab edits (label
  // updates, reorders) into one SecureStore write per ~300ms.
  Timer? _persistTabsTimer;
  static const _persistTabsDebounce = Duration(milliseconds: 300);
  // Set of tab models whose change-listener we've registered, so we
  // don't double-subscribe when restoring vs. spawning.
  final Set<TerminalTabModel> _tabsObserved = {};

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
      final idleSec = await ref.read(persistedIdleTimeoutProvider.future);
      final session = await PeershSession.open(
        bridge: bridge,
        server: connectServer,
        firebaseIdToken: idToken,
        idleTimeoutSec: idleSec,
      );
      if (!mounted) {
        await session.close();
        return;
      }
      // Probe persisted PTYs the host is holding for this connection.
      // If any match a tab snapshot we saved last run, restore the
      // whole tab strip in one shot (handles + order + active index)
      // without surfacing a picker.
      List<PtyHandleInfo> persisted = const [];
      try {
        persisted = await bridge.listPtys(sessionId: session.id);
      } catch (_) {}
      final restored = isReconnect
          ? false
          : await _restoreTabsFromSnapshot(persisted);
      setState(() {
        _session = session;
        _connState = _ConnState.connected;
        _reconnectAttempts = 0;
        _connectError = null;
        if (_tabs.isNotEmpty) return;
        if (persisted.where((p) => !p.attached).isEmpty) {
          // No reattachable PTYs — auto-spawn a fresh shell.
          _tabs.add(TerminalTabModel(initialLabel: 'shell'));
          _observeTab(_tabs.last);
        }
        // Otherwise leave _tabs empty so the user picks via the prompt.
      });
      // Foreground service keeps the OS from freezing the app process
      // (and the QUIC keepalive) when backgrounded. Opt-in via the
      // "Keep connection alive in background" setting (default off);
      // when off we skip both the persistent notification and the
      // POST_NOTIFICATIONS prompt — FCM-driven completion notices and
      // the auto-reattach + ring-buffer replay cover the typical
      // "left it backgrounded" case without the battery hit.
      final keepAlive =
          await ref.read(persistedBgKeepaliveProvider.future);
      if (keepAlive) {
        if (!isReconnect) {
          unawaited(_ensureNotificationPermission(bridge));
        }
        unawaited(bridge.startForegroundService(
          title: widget.server.name,
          body: 'Connected — tap to return',
        ));
      }
      if (isReconnect) _showReattachBanner();
      if (restored) _showReattachBanner();
      if (mounted && _tabs.isEmpty) {
        await _maybeOfferReattach(persisted);
      }
      if (!isReconnect && widget.pendingTabLabel.isNotEmpty) {
        _scheduleDeepLinkSelect();
      }
    } catch (e) {
      if (!mounted) return;
      _scheduleReconnectOrFail('$e');
    }
  }

  /// After a tap-deep-link cold/warm start, the matching tab's label is
  /// only set once the per-tab cwd probe runs (~2 s after PTY open).
  /// Poll briefly so we can switch focus once the label settles. Bails
  /// out after [_deepLinkSelectAttempts] attempts to avoid hanging on a
  /// tab that simply isn't there.
  static const _deepLinkSelectAttempts = 16;
  static const _deepLinkSelectInterval = Duration(milliseconds: 500);
  Timer? _deepLinkSelectTimer;
  int _deepLinkSelectTries = 0;

  void _scheduleDeepLinkSelect() {
    _deepLinkSelectTimer?.cancel();
    _deepLinkSelectTries = 0;
    _deepLinkSelectTimer = Timer.periodic(_deepLinkSelectInterval, (t) {
      if (!mounted) {
        t.cancel();
        return;
      }
      _deepLinkSelectTries++;
      final target = widget.pendingTabLabel;
      final idx = _tabs.indexWhere((m) => m.label == target);
      if (idx >= 0) {
        t.cancel();
        if (_activeIndex != idx) {
          setState(() => _activeIndex = idx);
        }
        return;
      }
      if (_deepLinkSelectTries >= _deepLinkSelectAttempts) {
        t.cancel();
      }
    });
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

  /// First-connect-only: trigger the POST_NOTIFICATIONS system prompt
  /// when needed, and surface a one-time hint banner if the user
  /// dismisses it. Doing this before the FG service starts means the
  /// notification appears on the first session. The hint state is
  /// purely UI — denial doesn't stop the service from running.
  Future<void> _ensureNotificationPermission(PeershBridge bridge) async {
    final ok = await bridge.notificationsEnabled();
    if (ok) return;
    await bridge.requestNotifications();
    // Re-check after a brief settle so the dialog has a chance to land.
    await Future<void>.delayed(const Duration(milliseconds: 500));
    if (!mounted) return;
    final stillOff = !(await bridge.notificationsEnabled());
    if (stillOff && mounted) {
      setState(() => _showNotifHint = true);
    }
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

  /// On first connect (not reconnect), pull the stored TabsSnapshot for
  /// this server and rebuild [_tabs] from any entries whose handle is
  /// still alive on the host. Stale handles get silent-forgotten so the
  /// snapshot doesn't keep retrying them. Returns true when at least
  /// one tab was restored.
  Future<bool> _restoreTabsFromSnapshot(
      List<PtyHandleInfo> persisted) async {
    final tabsState =
        await ref.read(persistedTabsProvider.future);
    final stored = tabsState.forServer(widget.server.id);
    if (stored == null || stored.tabs.isEmpty) return false;
    final liveHandles = {
      for (final p in persisted) p.handle,
    };
    final restoredEntries = <TabEntry>[];
    final stale = <String>[];
    for (final entry in stored.tabs) {
      if (entry.handle.isEmpty) continue;
      if (liveHandles.contains(entry.handle)) {
        restoredEntries.add(entry);
      } else {
        stale.add(entry.handle);
      }
    }
    if (restoredEntries.isEmpty) {
      // Whole snapshot has expired host-side. Drop it so the next
      // round doesn't keep replaying ghost handles.
      await ref
          .read(persistedTabsProvider.notifier)
          .clear(serverId: widget.server.id);
      return false;
    }
    for (final h in stale) {
      await ref
          .read(persistedTabsProvider.notifier)
          .forget(serverId: widget.server.id, handle: h);
    }
    final restoredTabs = <TerminalTabModel>[];
    for (final entry in restoredEntries) {
      final t = TerminalTabModel(
          initialLabel: entry.label.isEmpty ? 'shell' : entry.label);
      t.reattachHandle = entry.handle;
      restoredTabs.add(t);
      _observeTab(t);
    }
    setState(() {
      _tabs.addAll(restoredTabs);
      _activeIndex =
          stored.activeIndex.clamp(0, restoredTabs.length - 1);
    });
    return true;
  }

  /// Subscribe to [tab]'s ChangeNotifier once so label / handle / cwd
  /// updates trigger a debounced snapshot write. Idempotent — a tab
  /// already in [_tabsObserved] is a no-op.
  void _observeTab(TerminalTabModel tab) {
    if (!_tabsObserved.add(tab)) return;
    tab.addListener(_scheduleTabsPersist);
  }

  /// Coalesce a burst of mutations into a single SecureStore write.
  void _scheduleTabsPersist() {
    _persistTabsTimer?.cancel();
    _persistTabsTimer = Timer(_persistTabsDebounce, _persistTabsNow);
  }

  Future<void> _persistTabsNow() async {
    if (!mounted) return;
    final entries = <TabEntry>[
      for (final t in _tabs)
        if (t.reattachHandle.isNotEmpty)
          TabEntry(
            handle: t.reattachHandle,
            label: t.label,
            lastCwd: t.lastCwd,
          ),
    ];
    final active = entries.isEmpty
        ? 0
        : _activeIndex.clamp(0, entries.length - 1);
    try {
      await ref.read(persistedTabsProvider.notifier).save(
            serverId: widget.server.id,
            snapshot: TabsSnapshot(activeIndex: active, tabs: entries),
          );
    } catch (e) {
      debugPrint('peersh: persist tabs failed: $e');
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
      setState(() {
        final t = TerminalTabModel(initialLabel: 'shell');
        _tabs.add(t);
        _observeTab(t);
      });
    } else {
      setState(() {
        final tab = TerminalTabModel(initialLabel: 'reattaching…');
        tab.reattachHandle = picked;
        _tabs.add(tab);
        _observeTab(tab);
      });
    }
    _scheduleTabsPersist();
  }

  @override
  void dispose() {
    _reconnectTimer?.cancel();
    _resumedBannerTimer?.cancel();
    _deepLinkSelectTimer?.cancel();
    _persistTabsTimer?.cancel();
    _ptyExitWatcher?.cancel();
    final bridge = ref.read(bridgeProvider);
    unawaited(bridge.stopForegroundService());
    for (final tab in _tabs) {
      if (_tabsObserved.remove(tab)) {
        tab.removeListener(_scheduleTabsPersist);
      }
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
        _observeTab(tab);
      });
      _scheduleTabsPersist();
    }
  }

  void _spawnNewTab() {
    setState(() {
      _tabSeq += 1;
      final t = TerminalTabModel(initialLabel: 'tab $_tabSeq');
      _tabs.add(t);
      _activeIndex = _tabs.length - 1;
      _observeTab(t);
    });
    _scheduleTabsPersist();
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
    if (_tabsObserved.remove(removed)) {
      removed.removeListener(_scheduleTabsPersist);
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
    _scheduleTabsPersist();
  }

  Future<void> _toggleNotify(int idx) async {
    if (idx < 0 || idx >= _tabs.length) return;
    final tab = _tabs[idx];
    final ptyId = tab.ptyId;
    if (ptyId == null) return; // tab not yet bound to a PTY
    final newEnabled = !tab.notifyOnPromptReady;
    setState(() => tab.notifyOnPromptReady = newEnabled);
    try {
      final mobileId = await readOrCreateMobileDeviceId();
      await ref.read(bridgeProvider).ptyNotificationConfig(
            ptyId: ptyId,
            enabled: newEnabled,
            thresholdSeconds: tab.notifyThreshold.inSeconds,
            idleSeconds: tab.notifyIdleWindow.inSeconds,
            tabLabel: tab.label,
            mobileDeviceId: mobileId,
          );
      await _persistNotify(tab, enabled: newEnabled);
    } catch (e) {
      debugPrint('peersh: ptyNotificationConfig failed: $e');
      if (!mounted) return;
      // revert local state on failure so the bell reflects truth
      setState(() => tab.notifyOnPromptReady = !newEnabled);
    }
  }

  /// Long-press on the bell — let the user override threshold + idle
  /// window for this tab. Saves the new values, re-pushes config to the
  /// host (only if the tab already has the bell on), and persists.
  Future<void> _editNotifySettings(int idx) async {
    if (idx < 0 || idx >= _tabs.length) return;
    final tab = _tabs[idx];
    final result = await showDialog<_NotifyEditResult>(
      context: context,
      builder: (ctx) => _NotifyEditDialog(
        initialThresholdSec: tab.notifyThreshold.inSeconds,
        initialIdleSec: tab.notifyIdleWindow.inSeconds,
        tabLabel: tab.label,
      ),
    );
    if (result == null || !mounted) return;
    setState(() {
      tab.notifyThreshold = Duration(seconds: result.thresholdSec);
      tab.notifyIdleWindow = Duration(seconds: result.idleSec);
    });
    final ptyId = tab.ptyId;
    if (ptyId != null && tab.notifyOnPromptReady) {
      try {
        final mobileId = await readOrCreateMobileDeviceId();
        await ref.read(bridgeProvider).ptyNotificationConfig(
              ptyId: ptyId,
              enabled: true,
              thresholdSeconds: result.thresholdSec,
              idleSeconds: result.idleSec,
              tabLabel: tab.label,
              mobileDeviceId: mobileId,
            );
      } catch (e) {
        debugPrint('peersh: ptyNotificationConfig (edit) failed: $e');
      }
    }
    await _persistNotify(tab, enabled: tab.notifyOnPromptReady);
  }

  Future<void> _persistNotify(TerminalTabModel tab,
      {required bool enabled}) async {
    if (tab.reattachHandle.isEmpty) return;
    try {
      await ref.read(persistedNotifyConfigsProvider.notifier).upsert(
            serverId: widget.server.id,
            handle: tab.reattachHandle,
            config: NotifyConfig(
              enabled: enabled,
              thresholdSec: tab.notifyThreshold.inSeconds,
              idleSec: tab.notifyIdleWindow.inSeconds,
            ),
          );
    } catch (e) {
      debugPrint('peersh: persist notify config failed: $e');
    }
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

  String _keepAliveSubtitle() {
    final sec = ref.read(persistedIdleTimeoutProvider).valueOrNull ??
        PersistedIdleTimeoutNotifier.defaultSeconds;
    if (sec >= 24 * 60 * 60) {
      final days = sec ~/ (24 * 60 * 60);
      return 'Server keeps the shell for ~$days day${days == 1 ? '' : 's'}.';
    }
    if (sec >= 60 * 60) {
      final hours = sec ~/ (60 * 60);
      return 'Server keeps the shell for ~$hours hour${hours == 1 ? '' : 's'}.';
    }
    final mins = sec ~/ 60;
    return 'Server keeps the shell for ~$mins minute${mins == 1 ? '' : 's'}.';
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
      appBar: _fullscreen
          ? null
          : AppBar(
              title: Text(widget.server.name),
              actions: [
                IconButton(
                  tooltip: 'New tab',
                  onPressed: session == null ? null : _addTab,
                  icon: const Icon(Icons.add),
                ),
                IconButton(
                  tooltip: lineWrap
                      ? 'Switch to horizontal scroll'
                      : 'Switch to wrap',
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
                IconButton(
                  tooltip: 'Fullscreen',
                  onPressed: session == null
                      ? null
                      : () => setState(() => _fullscreen = true),
                  icon: const Icon(Icons.fullscreen),
                ),
              ],
            ),
      body: SafeArea(
        child: session == null
            ? const Center(child: CircularProgressIndicator())
            : Column(
                children: [
                  if (!_fullscreen && _showNotifHint)
                    _NotifHintBanner(
                      onOpenSettings: () =>
                          ref.read(bridgeProvider).openNotificationSettings(),
                      onDismiss: () =>
                          setState(() => _showNotifHint = false),
                    ),
                  if (!_fullscreen && _showResumedBanner)
                    const _ResumedBanner(),
                  if (!_fullscreen)
                    _TabBar(
                      tabs: _tabs,
                      activeIndex: _activeIndex,
                      keepAliveSubtitle: _keepAliveSubtitle(),
                      onTap: (i) {
                        setState(() => _activeIndex = i);
                        _scheduleTabsPersist();
                      },
                      onClose: _closeTab,
                      onKill: _killTabPty,
                      onToggleNotify: _toggleNotify,
                      onEditNotify: _editNotifySettings,
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
                  if (_fullscreen)
                    _FullscreenExitBar(
                      onExit: () => setState(() => _fullscreen = false),
                    )
                  else
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

/// Slim bar shown at the bottom of the terminal viewport while
/// fullscreen is active. Holds only the exit-fullscreen button so the
/// user has a discoverable way out without relying on a system gesture
/// or popping the entire route.
class _FullscreenExitBar extends StatelessWidget {
  const _FullscreenExitBar({required this.onExit});

  final VoidCallback onExit;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: Theme.of(context).colorScheme.surfaceContainer,
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: 36,
          child: Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              IconButton(
                tooltip: 'Exit fullscreen',
                icon: const Icon(Icons.fullscreen_exit),
                onPressed: onExit,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _NotifHintBanner extends StatelessWidget {
  const _NotifHintBanner({
    required this.onOpenSettings,
    required this.onDismiss,
  });

  final VoidCallback onOpenSettings;
  final VoidCallback onDismiss;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Material(
      color: scheme.tertiaryContainer,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        child: Row(
          children: [
            Icon(
              Icons.notifications_off_outlined,
              size: 18,
              color: scheme.onTertiaryContainer,
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                'Notifications are off. The connection-keeper notification is hidden; the session may be killed when backgrounded.',
                style: TextStyle(
                  color: scheme.onTertiaryContainer,
                  fontSize: 12,
                ),
              ),
            ),
            TextButton(
              onPressed: onOpenSettings,
              child: const Text('Settings'),
            ),
            IconButton(
              tooltip: 'Dismiss',
              onPressed: onDismiss,
              icon: const Icon(Icons.close, size: 18),
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
    required this.keepAliveSubtitle,
    required this.onTap,
    required this.onClose,
    required this.onKill,
    required this.onToggleNotify,
    required this.onEditNotify,
  });

  final List<TerminalTabModel> tabs;
  final int activeIndex;
  final String keepAliveSubtitle;
  final ValueChanged<int> onTap;
  final ValueChanged<int> onClose;
  final ValueChanged<int> onKill;
  final ValueChanged<int> onToggleNotify;
  final ValueChanged<int> onEditNotify;

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
                notifyEnabled: tab.notifyOnPromptReady,
                onTap: () => onTap(i),
                onClose: () => onClose(i),
                onToggleNotify: () => onToggleNotify(i),
                onLongPressNotify: () => onEditNotify(i),
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
                            subtitle: Text(keepAliveSubtitle),
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
    required this.notifyEnabled,
    required this.onTap,
    required this.onClose,
    required this.onLongPress,
    required this.onToggleNotify,
    required this.onLongPressNotify,
  });

  final String label;
  final bool active;
  final bool notifyEnabled;
  final VoidCallback onTap;
  final VoidCallback onClose;
  final VoidCallback onLongPress;
  final VoidCallback onToggleNotify;
  final VoidCallback onLongPressNotify;

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
              // Tap toggles bell on/off; long-press opens the threshold +
              // idle-window editor. IconButton has no onLongPress so we
              // wrap it in an InkWell that captures both gestures.
              SizedBox(
                width: 24,
                height: 24,
                child: InkWell(
                  onTap: onToggleNotify,
                  onLongPress: onLongPressNotify,
                  borderRadius: BorderRadius.circular(12),
                  child: Tooltip(
                    message: notifyEnabled
                        ? 'Notifications on — tap to turn off, long-press to edit'
                        : 'Notify when this tab\'s next command finishes — long-press to edit',
                    child: Icon(
                      notifyEnabled
                          ? Icons.notifications_active
                          : Icons.notifications_none,
                      size: 16,
                      color: fg,
                    ),
                  ),
                ),
              ),
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

/// Result returned by the bell long-press editor dialog.
class _NotifyEditResult {
  const _NotifyEditResult({required this.thresholdSec, required this.idleSec});
  final int thresholdSec;
  final int idleSec;
}

class _NotifyEditDialog extends StatefulWidget {
  const _NotifyEditDialog({
    required this.initialThresholdSec,
    required this.initialIdleSec,
    required this.tabLabel,
  });

  final int initialThresholdSec;
  final int initialIdleSec;
  final String tabLabel;

  @override
  State<_NotifyEditDialog> createState() => _NotifyEditDialogState();
}

class _NotifyEditDialogState extends State<_NotifyEditDialog> {
  late int _thresholdSec = widget.initialThresholdSec.clamp(1, 600);
  late int _idleSec = widget.initialIdleSec.clamp(0, 300);

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text('Notification settings — ${widget.tabLabel}'),
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Min command duration before firing: ${_thresholdSec}s'),
          Slider(
            value: _thresholdSec.toDouble(),
            min: 1,
            max: 120,
            divisions: 119,
            label: '${_thresholdSec}s',
            onChanged: (v) => setState(() => _thresholdSec = v.round()),
          ),
          const SizedBox(height: 8),
          Text(_idleSec == 0
              ? 'Idle-silence trigger: disabled'
              : 'Idle-silence trigger: ${_idleSec}s'),
          Slider(
            value: _idleSec.toDouble(),
            min: 0,
            max: 60,
            divisions: 60,
            label: _idleSec == 0 ? 'off' : '${_idleSec}s',
            onChanged: (v) => setState(() => _idleSec = v.round()),
          ),
          const SizedBox(height: 8),
          const Text(
            'Idle silence is for tools that don\'t print prompts (Claude, Codex). Leave at 0 for shells.',
            style: TextStyle(fontSize: 12),
          ),
        ],
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(
            context,
            _NotifyEditResult(
              thresholdSec: _thresholdSec,
              idleSec: _idleSec,
            ),
          ),
          child: const Text('Save'),
        ),
      ],
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
