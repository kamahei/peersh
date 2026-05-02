// Routes a pending tap-deep-link from FCM into the rest of the app.
//
// Capture happens at app startup (cold via getInitialMessage, warm via
// onMessageOpenedApp). The router stores the most recent unconsumed tap
// in a Riverpod state so any screen can react when it gains the
// foreground. ServersScreen watches the router and pushes the matching
// terminal screen; TerminalTabsScreen reads the pending tab_label after
// the connection settles and selects the right tab.

import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

class PendingNotification {
  const PendingNotification({
    required this.hostDeviceId,
    required this.tabLabel,
    required this.ptyId,
    required this.reason,
  });

  /// The host that originated the notification. Matches a saved server
  /// entry's `targetDeviceId`. Empty if the payload was malformed.
  final String hostDeviceId;

  /// Tab label at the time the notification fired. Used to pick the
  /// right tab once the session reattaches; an empty value means "open
  /// the server but don't change tab selection".
  final String tabLabel;

  /// Host-side PTY id (decimal). Best-effort secondary match; may not
  /// survive a host restart, in which case fall back to tab_label.
  final String ptyId;

  /// "prompt" or "idle" — purely informational for now.
  final String reason;

  static PendingNotification? fromMessage(RemoteMessage? m) {
    if (m == null) return null;
    final d = m.data;
    final hostId = (d['host_device_id'] ?? '').toString();
    if (hostId.isEmpty) return null;
    return PendingNotification(
      hostDeviceId: hostId,
      tabLabel: (d['tab_label'] ?? '').toString(),
      ptyId: (d['pty_id'] ?? '').toString(),
      reason: (d['reason'] ?? '').toString(),
    );
  }
}

class NotificationRouter extends Notifier<PendingNotification?> {
  @override
  PendingNotification? build() => null;

  void set(PendingNotification n) => state = n;

  /// Read-and-clear. Caller acts on the value, then any remaining
  /// listeners see null and stop firing on the same event.
  PendingNotification? consume() {
    final v = state;
    state = null;
    return v;
  }
}

final notificationRouterProvider =
    NotifierProvider<NotificationRouter, PendingNotification?>(
        NotificationRouter.new);
