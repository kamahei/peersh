// peersh Firebase Functions entry point.
//
// Phase 5 ships one function:
//   onSessionCreated  — Firestore trigger that fires when the signaling
//                       server creates users/{uid}/sessions/{sessionId}.
//                       It looks up the target device's fcm_token and
//                       sends a high-priority data message so the host
//                       can wake / re-establish its UDP socket.
//
// Cost guardrails (Phase 5+) live as separate functions in cost.ts and
// are wired in below when present.

import * as admin from 'firebase-admin';
import { onDocumentCreated } from 'firebase-functions/v2/firestore';
import { logger, setGlobalOptions } from 'firebase-functions/v2';

admin.initializeApp();

setGlobalOptions({
  // Region pinning matters for cost predictability; pick the same
  // region as your Firestore database.
  region: 'us-central1',
  // FCM Cloud Function should not be doing CPU-heavy work; one CPU is
  // plenty.
  cpu: 1,
  // Concurrency 80 is the v2 default and is fine for the I/O shape here.
  concurrency: 80,
  // Keep memory tight; this trigger does one Firestore read and one
  // FCM send.
  memory: '256MiB',
});

export const onSessionCreated = onDocumentCreated(
  'users/{userId}/sessions/{sessionId}',
  async (event) => {
    const snap = event.data;
    if (!snap) {
      logger.warn('onSessionCreated: no snapshot');
      return;
    }
    const data = snap.data();
    const userId = event.params.userId;
    const sessionId = event.params.sessionId;
    const hostDeviceId = data.host_device_id as string | undefined;
    if (!hostDeviceId) {
      logger.warn('onSessionCreated: session has no host_device_id', {
        userId,
        sessionId,
      });
      return;
    }

    const deviceRef = admin.firestore().doc(
      `users/${userId}/devices/${hostDeviceId}`,
    );
    const deviceSnap = await deviceRef.get();
    const token = deviceSnap.get('fcm_token') as string | undefined;
    if (!token) {
      logger.info('onSessionCreated: host has no fcm_token registered', {
        userId,
        hostDeviceId,
      });
      return;
    }

    try {
      await admin.messaging().send({
        token,
        // Data-only message: the host process handles wake-up itself
        // (no user-visible notification).
        data: {
          peerSh: 'wake',
          userId,
          sessionId,
          mobileDeviceId: data.mobile_device_id ?? '',
        },
        android: {
          priority: 'high',
          ttl: 30 * 1000, // wake-up is meaningful for ~30 s only
        },
        apns: {
          headers: {
            'apns-priority': '10',
          },
        },
      });
      logger.info('FCM wake sent', { userId, hostDeviceId, sessionId });
    } catch (err) {
      logger.error('FCM send failed', { err, userId, hostDeviceId });
    }
  },
);

// cost.ts hooks here when implemented:
// export { dailyBudgetGuard } from './cost';
