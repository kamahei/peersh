# Google Play "Data safety" answers

The Play Console asks a structured questionnaire about what data the
app collects and shares. These are the answers we'll submit. Update
each release if the answer set changes.

## Does your app collect or share any of the required user data types?

**No.**

The Android app saves operator-supplied configuration (server URL,
pre-shared key, persisted PTY handles, line-wrap setting) in the
Android Keystore via `flutter_secure_storage`. None of that data
leaves the device. There are no analytics, crash-reporter, or ad
SDKs in the build.

PowerShell commands and output that flow during a session are
transmitted **end-to-end encrypted between the user's phone and the
user's own PC over QUIC + TLS 1.3**. Google Play's Data Safety
guidelines define "collection" as data sent off the user's device to
servers controlled by the developer or a third-party SDK; peer-to-peer
traffic between two devices the user controls does not match that
definition (per Google's
[guidance](https://support.google.com/googleplay/android-developer/answer/10787469)).

## Is all of the user data collected by your app encrypted in transit?

**Yes.** TLS 1.3 inside QUIC, and WebSocket Secure (`wss://`) for the
signaling control channel.

## Do you provide a way for users to request that their data is deleted?

**Yes** — the user can:

1. Delete server entries in-app (long-press → Delete).
2. Uninstall the app (Android wipes all secure-storage entries
   tied to the app's UID).
3. Run `peersh-signaling psk revoke --user <id>` against their
   signaling server to revoke the PSK.

There is no developer-side delete request because we do not hold any
user data.

## Data types

For each row, the answer is "no, this app does not collect this":

- Personal info (name, email, ID, …)
- Financial info
- Health and fitness
- Messages
- Photos and videos
- Audio files
- Files and docs
- Calendar
- Contacts
- App activity
- Web browsing
- App info and performance (crash logs / diagnostics — we do **not**
  ship a crash-reporter SDK)
- Device or other IDs

## Security practices

- "Data is encrypted in transit": **Yes**.
- "You can request that data be deleted": **Yes** (see above).
- "Committed to follow the Google Play Families Policy": N/A
  (peersh is not directed at children).
- "Independent security review": **No** (open source — anyone can
  audit; we'll list a third-party audit here once one is performed).
