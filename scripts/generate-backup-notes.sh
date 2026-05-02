#!/usr/bin/env bash
# Generate local/backup-notes.md from whatever operator-specific files
# are currently on disk. Values it cannot determine automatically are
# left as `<unset>` placeholders for the operator to fill in.
#
# Run from the repo root (or anywhere — the script cd's into the
# repository root via its own path).
#
# Sources (each is optional; missing files just leave their fields empty):
#   firebase/.firebaserc                  -> Firebase project id
#   app/lib/firebase_options.dart         -> Firebase Web API key
#   app/firebase.json                     -> (existence indicator)
#   app/android/app/google-services.json  -> Project number, package name
#   app/android/key.properties            -> Release keystore alias / store path
#   local/peershd-build.env               -> ldflags inputs (project id, signaling URL, OAuth client)
#   local/firebase_metrics_token.txt      -> Cloud Run metrics token
#   ~/.android/debug.keystore             -> debug SHA-1 / SHA-256 (via keytool)
#   <release keystore>                    -> release SHA-1 / SHA-256 (via keytool)
#
# Output: local/backup-notes.md (overwritten on each run; commit out of
# tree — local/ is gitignored).

set -euo pipefail

cd "$(dirname "$0")/.."

OUT="local/backup-notes.md"
mkdir -p local

# Helper: read a key=value entry from a .env-style file. Echoes the
# value on stdout or empty string when not found.
env_get() {
  local file="$1" key="$2"
  if [[ ! -f "$file" ]]; then echo ""; return; fi
  awk -F= -v k="$key" '$1 == k { sub(/^[ \t]+|[ \t]+$/, "", $2); print $2; exit }' "$file"
}

# Helper: pull a quoted Dart string assignment, e.g.
#   apiKey: 'AIzaSy...'
# -> AIzaSy...
dart_string() {
  local file="$1" key="$2"
  if [[ ! -f "$file" ]]; then echo ""; return; fi
  grep -m1 -E "[\"']?${key}[\"']?\s*:" "$file" 2>/dev/null \
    | sed -E "s/.*[\"']?${key}[\"']?\s*:\s*[\"']([^\"']+)[\"'].*/\1/"
}

# Helper: pull a JSON value from google-services.json by jq-style key path.
# We avoid a hard jq dependency — Python is enough.
json_path() {
  local file="$1" path="$2"
  if [[ ! -f "$file" ]]; then echo ""; return; fi
  if ! command -v python >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1; then
    echo ""; return
  fi
  local py
  if command -v python3 >/dev/null 2>&1; then py=python3; else py=python; fi
  $py - "$file" "$path" <<'PY' 2>/dev/null || true
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    doc = json.load(f)
parts = sys.argv[2].split('.')
cur = doc
for p in parts:
    if p.endswith(']'):
        name, idx = p[:-1].split('[')
        cur = cur.get(name, [])[int(idx)]
    else:
        if isinstance(cur, dict):
            cur = cur.get(p, '')
        else:
            cur = ''
print(cur if isinstance(cur, str) else json.dumps(cur))
PY
}

# Helper: locate keytool. Prefers PATH, then JAVA_HOME, then common
# install locations on Windows + macOS + Linux. Windows paths get
# normalised to forward slashes so MSYS bash's `[[ -f ]]` works.
locate_keytool() {
  if command -v keytool >/dev/null 2>&1; then echo "keytool"; return; fi
  local cands=() jh
  if [[ -n "${JAVA_HOME:-}" ]]; then
    jh="${JAVA_HOME//\\//}"
    cands+=("$jh/bin/keytool" "$jh/bin/keytool.exe")
  fi
  if [[ -n "${LOCALAPPDATA:-}" ]]; then
    local lad="${LOCALAPPDATA//\\//}"
    # Locally-installed JDKs.
    for d in "$lad"/Programs/jdk*/bin/keytool.exe; do cands+=("$d"); done
    # Android Studio under %LOCALAPPDATA%.
    cands+=(
      "$lad/Programs/Android Studio/jbr/bin/keytool.exe"
      "$lad/Android/Sdk/jbr/bin/keytool.exe"
    )
  fi
  if [[ -n "${ProgramFiles:-}" ]]; then
    local pf="${ProgramFiles//\\//}"
    cands+=(
      "$pf/Android/Android Studio/jbr/bin/keytool.exe"
      "$pf/Java"/*/bin/keytool.exe
    )
  fi
  # ProgramFiles(x86) — bash can't reference the parens directly,
  # so fall back to the canonical path. Harmless when missing.
  cands+=("/c/Program Files (x86)/Android/Android Studio/jbr/bin/keytool.exe")
  # POSIX-y fallbacks.
  cands+=(
    "/Library/Internet Plug-Ins/JavaAppletPlugin.plugin/Contents/Home/bin/keytool"
    "/usr/lib/jvm/default-java/bin/keytool"
  )
  for c in "${cands[@]}"; do
    if [[ -x "$c" || -f "$c" ]]; then echo "$c"; return; fi
  done
  echo ""
}

# Helper: keytool fingerprints. Echoes "<SHA-1>\t<SHA-256>".
fingerprints() {
  local store="$1" alias="$2" pass="$3"
  local kt
  kt=$(locate_keytool)
  if [[ ! -f "$store" ]] || [[ -z "$kt" ]]; then
    echo -e "<unset>\t<unset>"
    return
  fi
  local raw
  raw=$("$kt" -list -v -keystore "$store" -alias "$alias" -storepass "$pass" -keypass "$pass" 2>/dev/null || true)
  if [[ -z "$raw" ]]; then echo -e "<unset>\t<unset>"; return; fi
  local sha1 sha256
  sha1=$(printf '%s\n' "$raw" | awk -F': ' '/^\s*SHA1:/ { print $2; exit }')
  sha256=$(printf '%s\n' "$raw" | awk -F': ' '/^\s*SHA256:/ { print $2; exit }')
  echo -e "${sha1:-<unset>}\t${sha256:-<unset>}"
}

# Helper: cache one `gradlew signingReport` run and emit "<SHA-1>\t<SHA-256>"
# for the requested variant ("debug" or "release"). Slow on the first
# call (~5-10s) but reliable when keytool isn't findable from this
# bash environment.
GRADLE_REPORT_CACHE=""
gradle_signing_report() {
  if [[ -n "$GRADLE_REPORT_CACHE" ]]; then return; fi
  if [[ ! -d app/android ]]; then GRADLE_REPORT_CACHE="<no-app-android>"; return; fi
  local gw="app/android/gradlew"
  [[ -f "$gw.bat" ]] && gw="$gw.bat"
  if [[ ! -x "$gw" && ! -f "$gw" ]]; then
    GRADLE_REPORT_CACHE="<no-gradlew>"
    return
  fi
  echo "  (running gradlew signingReport — this takes a few seconds...)" >&2
  local out
  out=$( (cd app/android && ./gradlew --quiet --console=plain signingReport) 2>&1 || true)
  if [[ -z "$out" ]]; then GRADLE_REPORT_CACHE="<empty>"; return; fi
  GRADLE_REPORT_CACHE="$out"
}

fingerprints_via_gradle() {
  local variant="$1"  # debug | release
  gradle_signing_report
  case "$GRADLE_REPORT_CACHE" in
    "<"*">") echo -e "<unset>\t<unset>"; return ;;
  esac
  # signingReport blocks look like:
  #   Variant: debug
  #   Config: debug
  #   Store: ...
  #   Alias: ...
  #   ...
  #   SHA1: AB:CD:...
  #   SHA-256: 12:34:...
  # Walk the output, isolate the variant block, grab fingerprints.
  local block
  block=$(printf '%s\n' "$GRADLE_REPORT_CACHE" | awk -v v="$variant" '
    $0 ~ "^Variant: "v"$" { capture=1; next }
    /^Variant: / && capture { exit }
    capture { print }
  ')
  if [[ -z "$block" ]]; then echo -e "<unset>\t<unset>"; return; fi
  local sha1 sha256
  sha1=$(printf '%s\n' "$block" | awk -F': ' '/^\s*SHA1:/ { print $2; exit }')
  sha256=$(printf '%s\n' "$block" | awk -F': ' '/^\s*SHA-?256:/ { print $2; exit }')
  echo -e "${sha1:-<unset>}\t${sha256:-<unset>}"
}

# ---- gather values --------------------------------------------------

PROJECT_ID=""
if [[ -f firebase/.firebaserc ]] && command -v python >/dev/null 2>&1; then
  PROJECT_ID=$(python -c 'import json,sys; print(json.load(open("firebase/.firebaserc")).get("projects",{}).get("default",""))' 2>/dev/null || true)
fi
PROJECT_ID="${PROJECT_ID:-$(env_get local/peershd-build.env PEERSH_BUILD_FIREBASE_PROJECT_ID)}"

WEB_API_KEY=$(dart_string app/lib/firebase_options.dart apiKey)
WEB_API_KEY="${WEB_API_KEY:-$(env_get local/peershd-build.env PEERSH_BUILD_FIREBASE_API_KEY)}"

PROJECT_NUMBER=$(json_path app/android/app/google-services.json project_info.project_number)
PACKAGE_NAME=$(json_path app/android/app/google-services.json "client[0].client_info.android_client_info.package_name")

SIGNALING_URL=$(env_get local/peershd-build.env PEERSH_BUILD_SIGNALING_URL)
REGION=$(env_get local/peershd-build.env PEERSH_BUILD_FIREBASE_REGION)
REGION="${REGION:-asia-northeast1}"

OAUTH_CLIENT_ID=$(env_get local/peershd-build.env PEERSH_BUILD_GOOGLE_CLIENT_ID)
OAUTH_CLIENT_SECRET=$(env_get local/peershd-build.env PEERSH_BUILD_GOOGLE_CLIENT_SECRET)

METRICS_TOKEN=""
if [[ -f local/firebase_metrics_token.txt ]]; then
  METRICS_TOKEN=$(tr -d '[:space:]' < local/firebase_metrics_token.txt)
fi

VERSION=$(env_get local/peershd-build.env PEERSH_BUILD_VERSION)
UPDATE_REPO=$(env_get local/peershd-build.env PEERSH_BUILD_UPDATE_REPO)

# Debug keystore (per-machine).
DEBUG_KS="${HOME}/.android/debug.keystore"
[[ -f "$DEBUG_KS" ]] || DEBUG_KS="${USERPROFILE:-$HOME}/.android/debug.keystore"
DEBUG_FP=$(fingerprints "$DEBUG_KS" androiddebugkey android)
DEBUG_SHA1=${DEBUG_FP%	*}
DEBUG_SHA256=${DEBUG_FP#*	}
if [[ "$DEBUG_SHA1" == "<unset>" ]]; then
  DEBUG_FP=$(fingerprints_via_gradle debug)
  DEBUG_SHA1=${DEBUG_FP%	*}
  DEBUG_SHA256=${DEBUG_FP#*	}
fi

# Release keystore (from key.properties).
RELEASE_SHA1="<unset>"
RELEASE_SHA256="<unset>"
RELEASE_KS_PATH=""
if [[ -f app/android/key.properties ]]; then
  RELEASE_STORE=$(env_get app/android/key.properties storeFile)
  RELEASE_ALIAS=$(env_get app/android/key.properties keyAlias)
  RELEASE_PASS=$(env_get app/android/key.properties storePassword)
  RELEASE_KEY_PASS=$(env_get app/android/key.properties keyPassword)
  if [[ -n "$RELEASE_STORE" ]]; then
    # storeFile may be relative to app/android.
    case "$RELEASE_STORE" in
      /* | ?:[/\\]*) RELEASE_KS_PATH="$RELEASE_STORE" ;;
      *)             RELEASE_KS_PATH="app/android/$RELEASE_STORE" ;;
    esac
    if [[ -f "$RELEASE_KS_PATH" && -n "$RELEASE_PASS" && -n "$RELEASE_ALIAS" ]]; then
      RELEASE_FP=$(fingerprints "$RELEASE_KS_PATH" "$RELEASE_ALIAS" "$RELEASE_PASS")
      RELEASE_SHA1=${RELEASE_FP%	*}
      RELEASE_SHA256=${RELEASE_FP#*	}
    fi
  fi
fi
# Even without key.properties, gradle's signingReport surfaces the
# "release" variant when the build.gradle has a release signingConfig
# wired (we always do — fallback to debug keystore). Try gradle when
# the direct keytool path didn't yield a result.
if [[ "$RELEASE_SHA1" == "<unset>" ]]; then
  RELEASE_FP=$(fingerprints_via_gradle release)
  RELEASE_SHA1=${RELEASE_FP%	*}
  RELEASE_SHA256=${RELEASE_FP#*	}
fi

# peershd device id (from %LOCALAPPDATA%\peersh\dev\dev-cert.pem if present).
DEVICE_ID=""
if [[ -n "${LOCALAPPDATA:-}" && -f "${LOCALAPPDATA}/peersh/dev/dev-cert.pem" ]]; then
  if command -v python >/dev/null 2>&1; then
    DEVICE_ID=$(python - <<'PY' 2>/dev/null || true
import base64, hashlib, sys, re
try:
    pem = open(__import__('os').environ['LOCALAPPDATA'] + '/peersh/dev/dev-cert.pem').read()
except Exception:
    sys.exit(0)
m = re.search(r'-----BEGIN CERTIFICATE-----(.+?)-----END CERTIFICATE-----', pem, re.S)
if not m:
    sys.exit(0)
der = base64.b64decode(re.sub(r'\s+', '', m.group(1)))
# Take the SubjectPublicKeyInfo: this is approximate (we just hash the
# whole cert as a proxy). The mobile app actually uses
# sha256(publicKey)[:10] but extracting the publicKey requires a real
# X.509 parser. We deliberately leave this as a placeholder so the
# operator pastes the device_id from peershd's startup log.
PY
)
  fi
fi
[[ -z "$DEVICE_ID" ]] && DEVICE_ID="<paste from peershd startup log>"

# ---- placeholder rewrite -------------------------------------------

placeholder() { [[ -n "${1:-}" ]] && printf %s "$1" || printf '<unset>'; }

cat > "$OUT" <<EOF
# peersh — backup notes

Generated by \`scripts/generate-backup-notes.sh\` on $(date -u +'%Y-%m-%dT%H:%M:%SZ').
Re-run any time you change a secret to refresh.

> Keep this file in the same encrypted backup as the keystores / secrets it
> references. \`local/\` is gitignored so this stays out of source control.

## Project identity

| Item | Value |
|---|---|
| Firebase project id | \`$(placeholder "$PROJECT_ID")\` |
| Firebase project number | \`$(placeholder "$PROJECT_NUMBER")\` |
| Android package name | \`$(placeholder "$PACKAGE_NAME")\` |
| Firebase Web API key | \`$(placeholder "$WEB_API_KEY")\` |
| Cloud Functions region | \`$(placeholder "$REGION")\` |
| Cloud Run signaling URL | \`$(placeholder "$SIGNALING_URL")\` |
| Cloud Run \`PEERSH_SIGNALING_METRICS_TOKEN\` | \`$(placeholder "$METRICS_TOKEN")\` |
| GCP billing account | \`<unset>\` |

## OAuth (\`-firebase-login\`)

| Item | Value |
|---|---|
| Desktop client id | \`$(placeholder "$OAUTH_CLIENT_ID")\` |
| Desktop client secret | \`$(placeholder "$OAUTH_CLIENT_SECRET")\` |

## peershd distribution

| Item | Value |
|---|---|
| Embedded version | \`$(placeholder "$VERSION")\` |
| Embedded update repo | \`$(placeholder "$UPDATE_REPO")\` |

## Android signing fingerprints

Re-run \`./gradlew signingReport\` to recover these from the keystores.

| Keystore | Path | SHA-1 | SHA-256 |
|---|---|---|---|
| Debug (this machine) | \`${DEBUG_KS}\` | \`${DEBUG_SHA1}\` | \`${DEBUG_SHA256}\` |
| Release | \`$(placeholder "$RELEASE_KS_PATH")\` | \`${RELEASE_SHA1}\` | \`${RELEASE_SHA256}\` |

> Lose the release keystore and existing Play-Store-published APKs cannot
> be upgraded. Back up \`release.keystore\` + \`storePassword\` + \`keyPassword\`
> together — neither one alone is useful.

## peershd host (Windows)

| Item | Value |
|---|---|
| device_id | \`${DEVICE_ID}\` |
| dev cert | \`%LOCALAPPDATA%\\peersh\\dev\\dev-cert.pem\` |
| dev key | \`%LOCALAPPDATA%\\peersh\\dev\\dev-key.pem\` |
| Persisted refresh token | \`%LOCALAPPDATA%\\peersh\\firebase-refresh-token.txt\` |

> Backing up the entire \`%LOCALAPPDATA%\\peersh\\\` directory keeps the
> device_id stable across reinstalls — otherwise every mobile client's
> "Target device id" needs updating.

## What's NOT captured automatically

The script reads only files on disk. Anything below is up to you to fill in:

- **GCP billing account** — find it in GCP Console → Billing → My billing accounts.
- **Apple Developer team id** — N/A until iOS distribution is wired.
- **PSK distribution log** — which user got which \`local/<user>.psk\`. The
  script knows the files exist but does not record who you handed them to.
- **Firebase Cloud Functions deployment state** — \`firebase functions:list\`
  is the source of truth.

## Files referenced by this report

EOF

ls_or_missing() {
  for f in "$@"; do
    if [[ -e "$f" ]]; then
      echo "- ✅ \`$f\`"
    else
      echo "- ⛔ \`$f\` (missing)"
    fi
  done
}

{
  ls_or_missing \
    "firebase/.firebaserc" \
    "app/lib/firebase_options.dart" \
    "app/firebase.json" \
    "app/android/app/google-services.json" \
    "app/android/key.properties" \
    "${RELEASE_KS_PATH:-app/android/release.keystore}" \
    "local/peershd-build.env" \
    "local/firebase_metrics_token.txt" \
    "$DEBUG_KS"
} >> "$OUT"

echo "Wrote $OUT"
echo ""
echo "Summary of detection:"
[[ -n "$PROJECT_ID"        ]] && echo "  ✅ Firebase project id"        || echo "  ⛔ Firebase project id"
[[ -n "$WEB_API_KEY"       ]] && echo "  ✅ Web API key"                 || echo "  ⛔ Web API key"
[[ -n "$PROJECT_NUMBER"    ]] && echo "  ✅ Firebase project number"     || echo "  ⛔ Firebase project number"
[[ -n "$SIGNALING_URL"     ]] && echo "  ✅ Cloud Run signaling URL"     || echo "  ⛔ Cloud Run signaling URL"
[[ -n "$METRICS_TOKEN"     ]] && echo "  ✅ Metrics token"               || echo "  ⛔ Metrics token"
[[ -n "$OAUTH_CLIENT_ID"   ]] && echo "  ✅ OAuth client id"             || echo "  ⛔ OAuth client id"
[[ -n "$OAUTH_CLIENT_SECRET" ]] && echo "  ✅ OAuth client secret"       || echo "  ⛔ OAuth client secret"
[[ "$DEBUG_SHA1" != "<unset>" ]] && echo "  ✅ Debug keystore fingerprints" || echo "  ⛔ Debug keystore fingerprints"
[[ "$RELEASE_SHA1" != "<unset>" ]] && echo "  ✅ Release keystore fingerprints" || echo "  ⛔ Release keystore fingerprints"
echo ""
echo "Edit $OUT to fill in any <unset> values, then drop it into your encrypted backup."
