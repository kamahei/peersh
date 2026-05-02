# peersh mobile app — user manual

This is the day-to-day guide for someone who's been handed a built APK and needs to use peersh to reach a Windows PC.

For setting up the host side (peershd) or signaling server, see [`build.md`](build.md) and [`deploy/`](deploy/).

## Installing

The build process produces `app-debug.apk` or `app-release.apk` under `app/build/app/outputs/flutter-apk/`. Sideload via:

```sh
adb install -r app-release.apk
```

On Android 13+, the first session start prompts for **notifications** permission. Allow it — the persistent "peersh — connected" notification is what lets the OS keep the QUIC connection alive when you switch apps or lock the screen.

## Adding a server

Open the app → **Add server** (the "+" button on the empty state, or floating "+" in the servers list).

Fill in:

- **Display name** (optional). Free-form label for your own benefit.
- **Discover from hostname** (optional, when adding). Type the signaling host (e.g. `signaling.example.com`) and tap **Lookup**. The app fetches `/.well-known/peersh.json` from that host and pre-fills the rest. Skip if your operator gave you a `wss://` URL directly.
- **ws / wss URL** — the signaling endpoint your operator told you to use.
- **Auth provider** — pick one:
  - **PSK (self-host)** — paste the user id + hex PSK your operator gave you. The PSK is a 64-character hex string.
  - **Firebase (Google sign-in)** — leaves the user/PSK fields hidden. The first connect prompts you to sign in with Google.
- **Target device_id** — the 16-character ASCII id peershd printed at startup on the host PC. **Optional in Firebase mode** (the picker on connect will fill it in).
- **STUN server** — leave the default `stun.l.google.com:19302` unless your operator says otherwise. Empty disables STUN (LAN-only mode).

Tap **Save**.

## Connecting to a PC

Tap a server entry in the list.

### PSK servers

The app dials the signaling server, the host replies with its candidates, NAT punching runs, QUIC connects, and a PowerShell tab opens. Type commands as you would in a normal terminal.

### Firebase servers

The first connect prompts for Google sign-in. After signing in, if the server entry has no remembered device id, the app shows a **Pick a PC** bottom sheet listing every PC registered under your Google account (most-recently-seen first). Tap one to connect. The choice is remembered for next time.

To switch PCs without disconnecting first: tap the ⋮ menu next to the server in the servers list → **Switch PC**.

## Pairing a PC (Firebase mode)

Each PC running `peershd -pair-code` (or `peershd -firebase-login`) needs a one-time bootstrap. The pair-code path:

1. App → **Settings** → **Pair PC** → **Generate code**.
2. The app shows a 6-digit code with a 5-minute countdown.
3. On the PC, run:
   ```
   peershd.exe -pair-code <code>
   ```
   (Plus `-firebase-project`, `-firebase-api-key`, `-signaling` etc., or rely on values embedded by your distribution build — see [`deploy/firebase.md`](deploy/firebase.md).)
4. peershd persists a refresh token under `%LOCALAPPDATA%\peersh\`. Subsequent `peershd.exe` invocations don't need any flags from this dance.

Codes are single-use; if you mistype, generate a fresh one.

## Using the terminal

The terminal screen is a real `xterm`-emulated PTY backed by ConPTY on the host. Most things work as in a normal `pwsh` window.

### Tabs

- **+** in the app bar: open a new tab. If the host has any reattach-able PTYs (you previously closed-but-kept-alive a session, or the connection dropped), you'll see a picker for them.
- **Long-press a tab**: → **Close (keep alive)** detaches but keeps the PTY running on the host (you can reattach next time). → **Kill PTY** terminates the underlying `pwsh` process.
- **IndexedStack**: switching between tabs is free; backgrounded tabs keep streaming output into their scrollback.

### Wrap vs. horizontal scroll

The app bar's wrap icon toggles whether long lines wrap at the viewport edge or extend horizontally with scroll. You can override per tab and set a global default in **Settings → Default to wrap mode**.

### Special keys

The bottom bar has frequently-needed keys that are awkward on a phone keyboard: Esc, Ctrl, Tab, arrow keys, `Ctrl+C` / `Ctrl+D` / `Ctrl+L` / `Ctrl+Z`, PgUp / PgDn, Home, End. Tap to send.

### IME input

The leftmost button on the special-keys bar opens a multiline text input bottom sheet. Useful for languages whose IMEs don't behave well in a terminal cell grid (Japanese, Chinese, Korean). Toggle "Append Enter" to send a newline at the end. Submit with **Send**.

### File browser

The app bar's folder icon opens a file browser scoped to the active tab's PowerShell session. Navigate with the standard tap / back / "up" controls.

### Text viewer

The app bar's document icon (or "Open" in the file browser) reads a remote file via `Get-Content -Raw -Encoding UTF8` and shows it with:

- Search field with up / down match navigation + match counter
- Copy-all button
- Encoding + size meta in the bottom strip
- Optional syntax highlighting (off by default)

This is read-only; there's no file upload or general transfer.

## Reconnect / session continuity

If the connection drops (network blip, screen off too long, NAT mapping aged out), the app shows a **Reconnecting (N / 6)** spinner with the last error and a **Stop trying** button. Backoff schedule: 0.5 s, 1 s, 2 s, 4 s, 8 s, 16 s, then capped at 30 s.

On a successful reconnect, a brief "Session resumed" banner flashes and the existing tabs reattach to their host-side PTYs (cwd + scrollback preserved up to 256 KiB per session).

If the backoff exhausts (6 attempts), you get the manual error screen with **Retry**.

## Settings

Reach via the gear icon on the servers list.

- **Default to wrap mode** — global default for new tabs.
- **Terminal font size** — slider; 10 to 22.
- **Pair PC** (Firebase mode only, when enabled in this build).
- **Developer spike screen** — direct QUIC dial without signaling. For protocol-level debugging only; the average user has no reason to open this.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `Sign-in failed: PlatformException(sign_in_failed, ApiException: 10)` | The Android keystore signing this APK has a SHA-1 that Firebase doesn't know. See [`backup.md`](backup.md) for the fingerprint flow. |
| `signaling: register rejected by server: auth: psk: signature invalid` | Wrong PSK or wrong user id. Re-paste from the operator's record. |
| `app check: ...` rejected at register | The signaling server has `app_check_required = true` but this build's App Check token isn't valid for the current keystore. Either disable enforcement on the server temporarily or register the keystore's SHA-256 in Firebase Console → App Check. |
| Stuck on connect spinner forever | Check peershd's log on the host. If it shows `connection accepted` + `handshake complete` and nothing after, the issue is on the mobile side — pull `adb logcat --pid=$(adb shell pidof -s dev.peersh.app)` to see what the bridge says. |
| No PCs in the picker | peershd hasn't registered yet. Verify it's running and shows `registered with signaling server (firebase mode)`. Pull-down to refresh the picker. |
| Persistent "Notifications are off" hint | Notification permission denied on first run. Tap **Settings** in the hint banner to grant it; the foreground service still runs without the notification but the OS may freeze the process more aggressively. |

For host-side / signaling-side troubleshooting, see [`deploy/self-hosting.md`](deploy/self-hosting.md) "Troubleshooting".
