# peersh-host MSI installer

A WiX 4 source tree that produces a Windows MSI for `peershd` and the
matching developer client `peersh-cli`. Building from source is the
common path; pre-built MSIs ship attached to GitHub releases.

## What the MSI does

- Installs `peershd.exe` and `peersh-cli.exe` under
  `C:\Program Files\peersh\` (per-machine).
- Writes the install directory onto the system `PATH` so a fresh
  command prompt can find both binaries.
- Adds a Start menu shortcut that runs `peershd` in interactive mode —
  useful for the very first run when the operator wants to copy the
  generated `device_id` out of the log.
- Records an entry under "Apps & Features" so the install can be
  cleanly removed.
- Shows the Apache 2.0 LICENSE during the install wizard.

## What it intentionally does NOT do

- It does **not** register `peershd` as a Windows Service or as a
  Logon Task. Those are explicit-opt-in operator decisions; run
  `peershd -install` or `peershd -install-logon-task` separately
  after the MSI install completes.
- It does **not** bundle a PowerShell runtime. peersh defers to
  whichever `pwsh.exe` / `powershell.exe` is already on the host's
  `PATH`.
- It does **not** auto-update. Pull the next MSI from GitHub
  releases and run it; the installer's `MajorUpgrade` element handles
  the in-place replacement.

## Building locally

Prerequisites:

- Go 1.22+ on `PATH`
- .NET SDK on `PATH` (provides the `dotnet` tool)
- WiX 4: `dotnet tool install --global wix`
- WiX UI extension (auto-restored by the `wix build` step)

Then:

```cmd
scripts\build-msi.cmd 0.1.0
```

The output lands at `dist\peersh-host-0.1.0-x64.msi`. The script:

1. Builds `bin\peershd.exe` and `bin\peersh-cli.exe` for `windows/amd64`.
2. Regenerates `windows\installer\License.rtf` from the repo-root
   `LICENSE` file (RTF is what the WiX UI banner expects).
3. Invokes `wix build` against `peershd.wxs` with the WiX UI extension.

## Verifying

A reasonable smoke test on a fresh VM:

```cmd
msiexec /i dist\peersh-host-0.1.0-x64.msi /l*v dist\install.log
peershd version
```

Uninstall the same way:

```cmd
msiexec /x dist\peersh-host-0.1.0-x64.msi /qn /l*v dist\uninstall.log
```

Or via Settings → Apps → Installed apps → "peersh host" → Uninstall.

## Files

| File | Purpose |
|---|---|
| `peersh.wxs` | WiX 4 source describing the package, files, shortcut, PATH entry. |
| `License.rtf` | Generated from `LICENSE` at build time. Not committed. |
| `../../scripts/build-msi.cmd` | Driver script: build binaries + regen RTF + invoke `wix build`. |
