# peersh — Play Store listing copy

Paste-ready text for the Play Console "Store listing" section. Refresh
on each release; the bottom of the file has a What's new template.

## App title

```
peersh
```

(Max 30 chars on Play; we use 6.)

## Short description

```
Open your home PowerShell from your phone — peer-to-peer, no relay.
```

(Max 80 chars.)

## Full description

```
peersh runs PowerShell on your home Windows PC from your phone — directly,
peer-to-peer over QUIC, with no third-party relay server in the data path.
A small signaling server only helps the two endpoints find each other; it
never sees your commands or their output.

Use it for ad-hoc admin tasks (Get-Process, Stop-Service, restart-VM …)
or to attach to long-running CLI tools (claude, codex, npm, your own
scripts). The terminal speaks full xterm: arrow keys, Tab completion,
Ctrl+C, ANSI colours, alt-screen, and Unicode all flow end-to-end.

What you get
• Multi-tab interactive terminal with peersh-style soft-key bar
  (Esc / Ctrl / Tab / arrows / ^C ^D ^L ^Z / PgUp / PgDn / Home / End)
• Persistent shells that survive disconnects: leave the app, come back
  later, and reattach to your old terminal with the last 256 KiB of
  scrollback intact.
• Built-in file browser scoped to whichever directory you cd into
  in the live shell, plus a syntax-highlighted text viewer.
• IME bottom sheet for multi-line paste / Japanese input.
• Bring your own signaling server (self-host with one Docker command)
  or run it in Firebase mode for Google-sign-in + multi-PC picker.

Privacy
peersh does not have a cloud account, no analytics, no ads. Sign-in is
via your operator's pre-shared key today; later versions will offer
Google sign-in for the official hosted server. Your shell traffic
flows directly between your phone and your PC over QUIC + TLS 1.3.

Open source
Apache 2.0, source on GitHub:
https://github.com/kamahei/peersh
```

(Max 4000 chars.)

## Categorization

- **App category**: Tools
- **Tags**: developer, ssh, terminal, powershell, remote
- **Email**: `<contact-email>` — pick the OSS contact you want surfaced
  on the public Play listing.
- **Website**: https://github.com/kamahei/peersh
- **Privacy policy**: (hosted URL pointing at privacy-policy.md)

## What's new (release-specific)

Replace each release. Max 500 chars per locale.

```
0.1.0 (initial release)
- Interactive remote PowerShell over QUIC, no relay server.
- Multi-tab terminal with full xterm.dart ANSI rendering.
- Persistent shells with scrollback replay across disconnects.
- Session-scoped file browser + text viewer with syntax highlighting.
- Self-hosted signaling: bring your own peersh-signaling instance,
  or run the included Render.com / Cloud Run / Docker images.
```

## Localized variants

For Japanese (`ja-JP`):

```
peersh は、自宅 Windows PC の PowerShell を、リレー経由ではなく
P2P で直接スマホから操作できるオープンソース (Apache 2.0) ツール
です。signaling サーバはコネクションのセットアップにだけ使い、
コマンド本文は QUIC で暗号化されて 1:1 で流れます。

機能
• 複数タブの対話型ターミナル(peersh ライクなソフトキーバー)
• 接続を切っても、シェルとスクロールバック (256 KiB) を保持
• cwd 連動のファイルブラウザ + 構文ハイライト付きテキスト
  ビューワ
• IME ボトムシート(日本語入力 / 複数行貼り付け)

ローカル / 自分用 signaling は Docker か Render.com / GCP Cloud Run
で立てられます。Firebase モードに切り替えれば Google サインイン
+ 複数 PC ピッカーも使えます。

ソース: https://github.com/kamahei/peersh
```
