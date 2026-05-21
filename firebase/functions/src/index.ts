// peersh Firebase Functions entry point.
//
// Currently deployed:
//   mintPairingCode        — HTTPS callable. Authenticated mobile
//                            clients request a 6-digit code; the
//                            function mints a Custom Token for the
//                            calling uid and stores it in
//                            pairing_codes/{code} with a 5-min TTL.
//   claimPairingCode       — HTTPS callable. Unauthenticated peershd
//                            hosts post the code through a rate-limited
//                            endpoint; the function returns the cached
//                            Custom Token and deletes the doc (one-shot).
//   budgetGuard            — Pub/Sub triggered by Cloud Billing budget
//                            alerts. Writes ops/budget-state with
//                            {triggered: true, ...} when a configured
//                            threshold fires; onNotificationCreated
//                            checks the flag and short-circuits the
//                            FCM send so a runaway loop can't burn
//                            quota past the budget.
//   onNotificationCreated  — RTDB trigger that fires on
//                            users/{uid}/notifications/{id} writes.
//                            Reads the target mobile FCM token, sends
//                            an FCM notification message, deletes the
//                            source doc.

import * as admin from 'firebase-admin';
import { onRequest, type Request } from 'firebase-functions/v2/https';
import { onMessagePublished } from 'firebase-functions/v2/pubsub';
import { onValueCreated } from 'firebase-functions/v2/database';
import { logger, setGlobalOptions } from 'firebase-functions/v2';
import { createHash, randomInt } from 'node:crypto';

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
const PAIRING_CODE_CREATE_ATTEMPTS = 16;
const CLAIM_RATE_LIMIT_WINDOW_MS = 60 * 1000;
const CLAIM_RATE_LIMIT_MAX_ATTEMPTS = 10;
const CLAIM_RATE_LIMIT_DOC_TTL_MS = 24 * 60 * 60 * 1000;

function generatePairingCode(): string {
  // Six numeric digits — easy to read aloud / type. 000000 reserved.
  return randomInt(1, 1_000_000).toString().padStart(6, '0');
}

async function createPairingCodeDoc(
  uid: string,
  customToken: string,
  now: admin.firestore.Timestamp,
  expiresAt: admin.firestore.Timestamp,
): Promise<string> {
  for (let attempt = 0; attempt < PAIRING_CODE_CREATE_ATTEMPTS; attempt += 1) {
    const code = generatePairingCode();
    const ref = admin.firestore().doc(`pairing_codes/${code}`);
    const created = await admin.firestore().runTransaction(async (tx) => {
      const snap = await tx.get(ref);
      if (snap.exists) {
        const existingExpiresAt = snap.get('expires_at') as
          | admin.firestore.Timestamp
          | undefined;
        if (!existingExpiresAt || existingExpiresAt.toMillis() >= Date.now()) {
          return false;
        }
      }
      tx.set(ref, {
        uid,
        custom_token: customToken,
        created_at: now,
        expires_at: expiresAt,
      });
      return true;
    });
    if (created) return code;
  }
  throw new Error('pairing-code-space-busy');
}

function claimRateLimitDocID(req: Request): string {
  const remote =
    req.ip ||
    req.socket.remoteAddress ||
    req.get('x-forwarded-for') ||
    'unknown';
  return createHash('sha256').update(remote).digest('hex');
}

interface ClaimRateLimitResult {
  allowed: boolean;
  retryAfterSeconds: number;
}

async function consumeClaimAttempt(
  req: Request,
): Promise<ClaimRateLimitResult> {
  const nowMillis = Date.now();
  const now = admin.firestore.Timestamp.fromMillis(nowMillis);
  const ref = admin
    .firestore()
    .doc(`pairing_claim_rate_limits/${claimRateLimitDocID(req)}`);

  return admin.firestore().runTransaction(async (tx) => {
    const snap = await tx.get(ref);
    const data = snap.exists ? snap.data() : undefined;
    const windowStartedAt = data?.window_started_at as
      | admin.firestore.Timestamp
      | undefined;
    const attempts = Number(data?.attempts ?? 0);
    const windowExpired =
      !windowStartedAt ||
      nowMillis - windowStartedAt.toMillis() >= CLAIM_RATE_LIMIT_WINDOW_MS;

    if (windowExpired) {
      tx.set(ref, {
        attempts: 1,
        window_started_at: now,
        updated_at: now,
        expires_at: admin.firestore.Timestamp.fromMillis(
          nowMillis + CLAIM_RATE_LIMIT_DOC_TTL_MS,
        ),
      });
      return { allowed: true, retryAfterSeconds: 0 };
    }

    if (attempts >= CLAIM_RATE_LIMIT_MAX_ATTEMPTS) {
      const retryAfterSeconds = Math.max(
        1,
        Math.ceil(
          (windowStartedAt.toMillis() +
            CLAIM_RATE_LIMIT_WINDOW_MS -
            nowMillis) /
            1000,
        ),
      );
      return { allowed: false, retryAfterSeconds };
    }

    tx.set(
      ref,
      {
        attempts: attempts + 1,
        updated_at: now,
        expires_at: admin.firestore.Timestamp.fromMillis(
          nowMillis + CLAIM_RATE_LIMIT_DOC_TTL_MS,
        ),
      },
      { merge: true },
    );
    return { allowed: true, retryAfterSeconds: 0 };
  });
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
    const now = admin.firestore.Timestamp.now();
    const expiresAt = admin.firestore.Timestamp.fromMillis(
      now.toMillis() + PAIRING_CODE_TTL_SEC * 1000,
    );

    let code: string;
    try {
      code = await createPairingCodeDoc(uid, customToken, now, expiresAt);
    } catch (e) {
      logger.error('mintPairingCode: unable to allocate pairing code', {
        uid,
        err: `${e}`,
      });
      res.status(503).json({ error: 'pairing-code-unavailable' });
      return;
    }

    logger.info('pairing code minted', { uid, expiresAt });
    res.status(200).json({
      code,
      expires_at: expiresAt.toMillis(),
      ttl_seconds: PAIRING_CODE_TTL_SEC,
    });
  },
);

export const claimPairingCode = onRequest(
  { cors: false },
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
    const limit = await consumeClaimAttempt(req);
    if (!limit.allowed) {
      res.set('Retry-After', limit.retryAfterSeconds.toString());
      res.status(429).json({ error: 'too-many-requests' });
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
    logger.info('pairing code claimed', { uid: result.uid });
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

// onNotificationCreated dispatches a v2-B push notification when the
// host writes /users/{userId}/notifications/{notifId} into RTDB.
//
// Source doc shape:
//   { target_mobile_device_id, host_device_id, title, body,
//     deep_link: { ... }, created_at }
//
// The function reads the target mobile's FCM token from RTDB
// /users/{userId}/devices/{target_mobile_device_id}/fcm_token, sends
// an FCM notification message with a `data` payload carrying the
// deep-link, then deletes the source doc so the queue stays small.
//
// Same budget-guard short-circuit as onSessionCreated: ops/budget-
// state.triggered=true silently drops the dispatch (mobile users can
// re-toggle their bell when the operator clears the flag).
export const onNotificationCreated = onValueCreated(
  {
    ref: '/users/{userId}/notifications/{notifId}',
    // RTDB v2 triggers must run in the same region as the database
    // instance. Our RTDB lives in asia-southeast1 (see
    // docs/firebase-mode.md), while the rest of the functions pin to
    // asia-northeast1 — override here.
    region: 'asia-southeast1',
  },
  async (event) => {
    const data = event.data?.val();
    if (!data) {
      logger.warn('onNotificationCreated: empty snapshot');
      return;
    }
    const userId = event.params.userId as string;
    const notifId = event.params.notifId as string;

    const guard = await admin.firestore().doc('ops/budget-state').get();
    if (guard.exists && guard.get('triggered') === true) {
      logger.warn('onNotificationCreated: skipped, budget guard active', {
        userId,
        notifId,
      });
      // Still delete so the queue doesn't grow.
      await event.data?.ref.remove();
      return;
    }

    const targetId = data.target_mobile_device_id;
    if (!targetId || typeof targetId !== 'string') {
      logger.warn('onNotificationCreated: missing target_mobile_device_id', {
        userId,
        notifId,
      });
      await event.data?.ref.remove();
      return;
    }

    const tokenSnap = await admin
      .database()
      .ref(`/users/${userId}/devices/${targetId}/fcm_token`)
      .get();
    const token = tokenSnap.val();
    if (!token || typeof token !== 'string') {
      logger.info('onNotificationCreated: no fcm_token registered', {
        userId,
        targetId,
      });
      await event.data?.ref.remove();
      return;
    }

    // Stringify the deep_link map — FCM data values must be strings.
    const deepLink: Record<string, string> = {};
    if (data.deep_link && typeof data.deep_link === 'object') {
      for (const [k, v] of Object.entries(data.deep_link)) {
        deepLink[k] = String(v);
      }
    }

    try {
      await admin.messaging().send({
        token,
        notification: {
          title: data.title ?? 'peersh',
          body: data.body ?? '',
        },
        data: deepLink,
        android: {
          priority: 'high',
          notification: {
            channelId: 'command_ready',
          },
        },
        apns: {
          headers: { 'apns-priority': '10' },
          payload: {
            aps: { sound: 'default' },
          },
        },
      });
      logger.info('FCM notification sent', { userId, targetId, notifId });
    } catch (err) {
      logger.error('FCM send failed', { err, userId, targetId, notifId });
    } finally {
      // Cleanup the source doc whether send succeeded or not — a stuck
      // notification entry would otherwise re-fire on every cold start
      // of the function. Failed sends are dropped (no retry queue).
      await event.data?.ref.remove();
    }
  },
);
