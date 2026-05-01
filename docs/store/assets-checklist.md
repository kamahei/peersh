# Play Store asset checklist

Required graphic assets and where they come from.

## Required for first publish

| Asset | Spec | Source / status |
|---|---|---|
| App icon | 512 × 512 PNG, 32-bit, no rounded mask | `app/icon/google_play_icon_512.png` (regenerate via `scripts/build_launcher_icon.py`). |
| Feature graphic | 1024 × 500 PNG or JPEG | **TODO** — not generated yet. Plain background + the ">" caret + the wordmark, filling 1024×500. Easy to derive from the master icon. |
| Phone screenshots | min 2, max 8, JPEG or PNG, 16:9 or 9:16, 1080 px on the short side recommended | **TODO** — capture from a real Android device showing: (1) terminal mid-claude session, (2) tab bar with multiple shells, (3) file browser, (4) text viewer with syntax highlight, (5) settings / IME sheet. |
| 7-inch tablet screenshots | optional but recommended for a Tools app | **TODO** — same content as phone but tablet aspect. |
| Promo video (YouTube link) | optional | Skip for first release. |

## Optional polish (later releases)

- 7-inch and 10-inch tablet screenshots (drives upranking on tablet
  search).
- A Play Store listing experiment using a different feature graphic
  to A/B-test conversion.

## Asset generation tips

- The square icon master at `app/icon/peersh_imagegen_1024.png` is
  the source of truth. `scripts/build_launcher_icon.py` derives:
  - Android mipmaps (`mipmap-{m,h,xh,xxh,xxxh}dpi`)
  - The 512×512 Play store icon
  - `windows/assets/peersh.ico` and `peersh_256.png` (for the MSI)
- For the feature graphic, do not bake in any text that might be
  Localized — Play renders the title underneath the graphic.
- Screenshots should match the locale you're publishing — for `ja-JP`,
  capture with the device language set to Japanese so the AppBar
  labels read correctly.
