# peersh Firebase project

This directory contains the artifacts the **operator** of the official
hosted peersh signaling server deploys to Firebase / GCP. Self-hosters
who only run `peersh-signaling` in PSK + SQLite mode never need this.

## Layout

```
firebase/
├── firebase.json            # firebase CLI config (functions + rules)
├── firestore.rules          # per-user isolation rules
├── firestore.indexes.json   # composite indexes (none yet)
├── .firebaserc.example      # copy to .firebaserc and set your project id
└── functions/               # TypeScript Cloud Functions
    ├── src/index.ts         # FCM wake-up trigger (Phase 5)
    ├── package.json
    ├── tsconfig.json
    └── .eslintrc.cjs
```

## Prerequisites

- Firebase CLI: `npm install -g firebase-tools`
- Node.js 20 (matches the `runtime` in `firebase.json`)
- A Firebase project with Firestore (Native mode), Authentication,
  Cloud Messaging, and (optionally) App Check enabled. See
  `docs/firebase-setup.md` for a step-by-step.

## Quick reference

```sh
cd firebase
cp .firebaserc.example .firebaserc          # then edit project id
cd functions && npm install && cd ..

# Deploy everything
firebase deploy

# Or just one piece
firebase deploy --only firestore:rules
firebase deploy --only functions
```

## Cost guardrails

Phase 5 ships the FCM trigger only. Budget Alerts, App Engine Daily
Spending Limit, and the auto-disable-on-breach Cloud Function are
documented as Phase 5/7 follow-ups in `docs/firebase-setup.md`.
