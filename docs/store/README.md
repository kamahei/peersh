# peersh — Google Play store materials

Drafts for the Google Play submission. The Apple App Store side is
deferred until iOS device validation is feasible (macOS-only build
gate). F-Droid materials live alongside this directory once a release
is tagged.

## Files

| File | Purpose |
|---|---|
| [`play-listing.md`](play-listing.md) | Title, short / full description, tags, what's-new template. Paste-ready. |
| [`privacy-policy.md`](privacy-policy.md) | Required by Google Play. Host this somewhere reachable from the listing's "Privacy policy" URL field. |
| [`assets-checklist.md`](assets-checklist.md) | Graphic asset specs + status of each (icon, feature graphic, screenshots, content rating prompts). |
| [`data-safety.md`](data-safety.md) | Answers to Google Play's Data Safety questionnaire. |

## Submission checklist

The order Google Play prefers:

1. Create the app entry in Play Console (no upload required for this step).
2. Fill **Store listing** with content from `play-listing.md`.
3. Upload the assets per `assets-checklist.md`.
4. Fill **Data safety** with content from `data-safety.md`.
5. Fill **Content rating** questionnaire (peersh is a developer tool;
   the IARC will rate it Everyone / 3+).
6. Set **App content** → Privacy policy URL pointing at a hosted copy
   of `privacy-policy.md`.
7. Upload the signed AAB to **Production** track.
8. Submit for review.

A "closed testing" track is recommended for the first release so a
small set of test accounts can validate the flow before going public.
