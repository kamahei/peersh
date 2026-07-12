# Running a macOS host

A Mac can be a peersh **host** — the same role a Windows PC plays. `peershd` runs on the Mac, registers with your signaling server, and lets you drive the machine's shell from the iOS / Android client, peer-to-peer over QUIC. The only difference from the Windows host is the terminal backend:

- **Windows:** ConPTY, spawning PowerShell (`pwsh` / `powershell.exe`).
- **macOS:** a `forkpty` PTY (`github.com/creack/pty`), spawning your **login shell** (zsh / bash / sh, resolved from `$SHELL`) as a login + interactive shell.

The interactive PTY path — the real terminal — works fully on macOS. The legacy one-shot `exec.v1` (PowerShell) path is **Windows-only**; a Mac has no PowerShell, so a client that requests that legacy path gets a clean "not available" response and uses the PTY stream instead.

In the client's device picker each host appears with a **Mac / laptop icon** (Windows hosts get a desktop icon) and is labelled by its hostname — peershd writes `kind`, `display_name`, and `platform` (`mac`) into its device record.

> The signaling deployment is identical to the Windows path — pick a target from [`README.md`](README.md). The macOS host uses the same PSK or Firebase signaling you already run. Note the single-instance requirement for Cloud Run in [`cloud-run.md`](cloud-run.md).

## Prerequisites

- **Go 1.22+** on PATH (to build `peershd`).
- **Xcode command-line tools** (`xcode-select --install`).
- A signaling server reachable from the Mac (PSK or Firebase mode), plus its URL — see the guides linked from [`README.md`](README.md).

## Build

The host is pure Go, so it cross-compiles from any machine. On the Mac itself:

```sh
# Plain build (PSK only — no embedded Firebase defaults):
GOOS=darwin GOARCH=arm64 go build -o local/peershd ./windows/cmd/peershd

# Or a distribution build with embedded Firebase / OAuth defaults
# (reads local/peershd-build.env, same as the Windows build-peershd-distrib.sh):
cp scripts/peershd-build.env.example local/peershd-build.env
$EDITOR local/peershd-build.env
bash scripts/build-peershd-macos.sh              # → local/peershd (Mach-O)
bash scripts/build-peershd-macos.sh universal    # arm64 + amd64 fat binary
```

`local/` is gitignored. See [`../build.md`](../build.md) for the full build matrix.

## Run

### PSK mode

```sh
echo <hex> > alice.psk
./local/peershd -signaling wss://<host>/ws -user alice -psk-file alice.psk \
                -display-name "$(hostname)"
```

`peershd` logs its `device_id` at startup — add it (with the signaling URL + PSK) as a server entry in the mobile app, or pass it to `peersh-cli -target`.

### Firebase mode

```sh
./local/peershd -firebase-login -display-name "$(hostname)"
```

The first run opens a browser to sign in with Google and persists a refresh token; subsequent runs need no prompt. To bootstrap without a browser, use the mobile app's **Pair PC** screen and pass its 6-digit code with `-pair-code <code>`.

When peershd is running, it spawns your login shell — whatever `$SHELL` points at (zsh on a default modern macOS, or bash / sh), as a login + interactive session, so it inherits your `.zprofile` / `.zshrc` (or bash equivalents), PATH, and environment.

## Auto-start at login (LaunchAgent)

To make the Mac a host that comes up at every login, install `peershd` as a **per-user LaunchAgent**:

```sh
bash scripts/install-peershd-macos.sh
```

The script:

1. builds `peershd` with embedded Firebase config (`scripts/build-peershd-macos.sh`),
2. installs it to `~/.local/bin/peershd`,
3. bootstraps Firebase auth once — a browser sign-in, or set `PEERSH_PAIR_CODE=<6-digit code>` in the environment to use the mobile app's pair code instead,
4. registers and starts a LaunchAgent at `~/Library/LaunchAgents/peershd.plist` (`RunAtLoad` + `KeepAlive`) via kardianos/service.

The agent runs **as the logged-in user, not root**, so it has your shell, environment, and keychain — the macOS analogue of the Windows per-user logon task. Override the install directory with `PEERSH_BIN_DIR` (default `~/.local/bin`).

### Manage it

```sh
peershd -service-status     # is the agent loaded / running?
peershd -start              # start now
peershd -stop               # stop
peershd -install            # (re)register the LaunchAgent
peershd -uninstall          # deregister the LaunchAgent
```

Logs go to `~/peershd.out.log` and `~/peershd.err.log`.

### Uninstall

```sh
bash scripts/uninstall-peershd-macos.sh
```

This stops and deregisters the LaunchAgent (removing `~/Library/LaunchAgents/peershd.plist`). It leaves the binary and the persisted Firebase refresh token in place — delete `~/.local/bin/peershd` and `~/.local/share/peersh/` by hand if you want them gone too.

## See also

- [`../build.md`](../build.md) — building `peershd` (Windows + macOS) and the mobile clients.
- [`self-hosting.md`](self-hosting.md) — target-agnostic signaling operations (TLS, PSK lifecycle, config reference).
- [`firebase.md`](firebase.md) — Firebase mode setup (Google sign-in, pair codes).
- [`cloud-run.md`](cloud-run.md) — Cloud Run signaling deploy, including the single-instance requirement.
