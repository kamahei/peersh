// peersh Firebase Functions entry point.
//
// Phase 5 ships:
//   onSessionCreated   — Firestore trigger that fires when the signaling
//                        server creates users/{uid}/sessions/{sessionId}.
//                        Sends a high-priority FCM data message so the
//                        host can wake / re-establish its UDP socket.
//   mintPairingCode    — HTTPS callable. Authenticated mobile clients
//                        request a 6-digit code; the function mints a
//                        Custom Token for the calling uid and stores it
//                        in pairing_codes/{code} with a 5-min TTL.
//   claimPairingCode   — HTTPS callable. Unauthenticated peershd hosts
//                        post the code; the function returns the cached
//                        Custom Token and deletes the doc (one-shot).
//   budgetGuard        — Pub/Sub triggered by Cloud Billing budget
//                        alerts. Writes ops/budget-state with
//                        {triggered: true, ...} when a configured
//                        threshold fires; onSessionCreated checks the
//                        flag and short-circuits the FCM send so a
//                        runaway loop can't burn quota past the budget.

import * as admin from 'firebase-admin';
import { onDocumentCreated } from 'firebase-functions/v2/firestore';
import { onRequest } from 'firebase-functions/v2/https';
import { onMessagePublished } from 'firebase-functions/v2/pubsub';
import { logger, setGlobalOptions } from 'firebase-functions/v2';

admin.initializeApp();

setGlobalOptions({
  // Region pinning matters for cost predictability; pick the same
  // region as your Firestore database.
  region: 'asia-northeast1',
  // FCM Cloud Function should not be doing CPU-heavy work; one CPU is
  // plenty.
  cpu: 1,
  // Concurrency 80 is the v2 default and is fine for the I/O shape here.
  concurrency: 80,
  // Keep memory tight; this trigger does one Firestore read and one
  // FCM send.
  memory: '256MiB',
});

// onSessionCreated is currently dead code: no client writes
// users/{uid}/sessions/{sid}. The wake path used by service-account-mode
// peershd reads users/{uid}/wake_requests/{rid} (written directly by the
// mobile client, see app/lib/services/peersh_session.dart). This trigger
// is kept for a future v2 that may centralize wake fan-out and budget
// enforcement on the server side.
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

    // Cost guardrail: when budgetGuard has marked the project as over
    // budget, skip the FCM send entirely. Mobile clients still get
    // their own copy of the session via Firestore — they just won't
    // wake up an idle host. Operator clears the flag to resume.
    const guard = await admin.firestore().doc('ops/budget-state').get();
    if (guard.exists && guard.get('triggered') === true) {
      logger.warn('onSessionCreated: skipped, budget guard active', {
        userId,
        sessionId,
        threshold: guard.get('threshold'),
      });
      return;
    }

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

// ---------------------------------------------------------------------
// Pairing flow
// ---------------------------------------------------------------------
//
// Replaces the "ship a service-account JSON to every host PC" model.
// On the mobile side a signed-in user requests a short-lived 6-digit
// code; on the PC side `peershd -pair-code <code>` claims the code,
// receives a Custom Token, and exchanges it (via Identity Toolkit) for
// an ID + Refresh Token pair. Only the Refresh Token is persisted on
// disk, scoped to that single uid.
//
// Storage: pairing_codes/{code} = {
//     uid: string,
//     custom_token: string,
//     created_at: Timestamp,
//     expires_at: Timestamp,
// }
// firestore.rules denies all client access to this collection — both
// functions use the Admin SDK which bypasses the rules.

const PAIRING_CODE_TTL_SEC = 5 * 60;

function generatePairingCode(): string {
  // Six numeric digits — easy to read aloud / type. 000000 reserved.
  for (;;) {
    const n = Math.floor(Math.random() * 1_000_000);
    if (n === 0) continue;
    return n.toString().padStart(6, '0');
  }
}

export const mintPairingCode = onRequest(
  { cors: true },
  async (req, res) => {
    if (req.method !== 'POST') {
      res.status(405).json({ error: 'method-not-allowed' });
      return;
    }
    const authHeader = req.get('authorization') || '';
    const m = authHeader.match(/^Bearer\s+(.+)$/i);
    if (!m) {
      res.status(401).json({ error: 'missing-id-token' });
      return;
    }
    let decoded: admin.auth.DecodedIdToken;
    try {
      decoded = await admin.auth().verifyIdToken(m[1]);
    } catch (e) {
      logger.warn('mintPairingCode: invalid id token', { err: `${e}` });
      res.status(401).json({ error: 'invalid-id-token' });
      return;
    }
    const uid = decoded.uid;

    const customToken = await admin.auth().createCustomToken(uid);
    const code = generatePairingCode();
    const now = admin.firestore.Timestamp.now();
    const expiresAt = admin.firestore.Timestamp.fromMillis(
      now.toMillis() + PAIRING_CODE_TTL_SEC * 1000,
    );

    await admin.firestore().doc(`pairing_codes/${code}`).set({
      uid,
      custom_token: customToken,
      created_at: now,
      expires_at: expiresAt,
    });

    logger.info('pairing code minted', { uid, code, expiresAt });
    res.status(200).json({
      code,
      expires_at: expiresAt.toMillis(),
      ttl_seconds: PAIRING_CODE_TTL_SEC,
    });
  },
);

export const claimPairingCode = onRequest(
  { cors: true },
  async (req, res) => {
    if (req.method !== 'POST') {
      res.status(405).json({ error: 'method-not-allowed' });
      return;
    }
    const code = ((req.body && req.body.code) || '').toString().trim();
    if (!/^\d{6}$/.test(code)) {
      res.status(400).json({ error: 'invalid-code' });
      return;
    }
    const ref = admin.firestore().doc(`pairing_codes/${code}`);
    const result = await admin.firestore().runTransaction(async (tx) => {
      const snap = await tx.get(ref);
      if (!snap.exists) return { ok: false, reason: 'not-found' as const };
      const data = snap.data()!;
      const expiresAt = data.expires_at as admin.firestore.Timestamp;
      if (!expiresAt || expiresAt.toMillis() < Date.now()) {
        tx.delete(ref);
        return { ok: false, reason: 'expired' as const };
      }
      tx.delete(ref);
      return {
        ok: true,
        uid: data.uid as string,
        customToken: data.custom_token as string,
      };
    });

    if (!result.ok) {
      res.status(404).json({ error: result.reason });
      return;
    }
    logger.info('pairing code claimed', { uid: result.uid, code });
    res.status(200).json({
      uid: result.uid,
      custom_token: result.customToken,
    });
  },
);

// ---------------------------------------------------------------------
// Cost guardrail
// ---------------------------------------------------------------------
//
// Wire-up:
//   1. Operator creates a Cloud Billing budget at GCP Console -> Billing
//      -> Budgets & alerts. Set thresholds (e.g. 50%, 90%, 100%) and
//      attach a Pub/Sub topic — convention name `peersh-budget-alert`.
//   2. The budget service publishes JSON messages on that topic at each
//      threshold. budgetGuard reads them and writes ops/budget-state.
//   3. onSessionCreated reads ops/budget-state on every fire; when the
//      `triggered` flag is true, it skips the FCM send. Operator clears
//      the flag (delete the doc or set triggered: false) to resume.
//
// Threshold defaults to firing at 100% of the budget; operators can
// tighten via PEERSH_BUDGET_GUARD_THRESHOLD env var (e.g. "0.9" for
// 90%).

const BUDGET_GUARD_THRESHOLD = parseFloat(
  process.env.PEERSH_BUDGET_GUARD_THRESHOLD ?? '1.0',
);

interface BudgetAlert {
  budgetDisplayName?: string;
  costAmount?: number;
  budgetAmount?: number;
  alertThresholdExceeded?: number;
  costIntervalStart?: string;
}

export const budgetGuard = onMessagePublished(
  'peersh-budget-alert',
  async (event) => {
    let alert: BudgetAlert = {};
    try {
      alert = event.data.message.json as BudgetAlert;
    } catch {
      // Pub/Sub message wasn't JSON; nothing to do.
      logger.warn('budgetGuard: non-JSON message body');
      return;
    }
    const cost = alert.costAmount ?? 0;
    const budget = alert.budgetAmount ?? 0;
    const ratio = budget > 0 ? cost / budget : 0;
    logger.info('budget alert received', { cost, budget, ratio });

    if (ratio >= BUDGET_GUARD_THRESHOLD) {
      await admin.firestore().doc('ops/budget-state').set(
        {
          triggered: true,
          ratio,
          cost_amount: cost,
          budget_amount: budget,
          threshold: BUDGET_GUARD_THRESHOLD,
          updated_at: admin.firestore.FieldValue.serverTimestamp(),
        },
        { merge: true },
      );
      logger.warn('budget guard activated', { ratio });
      return;
    }

    // Below threshold: leave any existing state alone but log progress.
    logger.info('budget under threshold', { ratio });
  },
);
