#!/usr/bin/env bash
# wpt-sync.sh — refresh the vendored web-platform-tests subset under testdata/wpt/.
#
# unblink measures its standards compatibility against a PINNED, VENDORED slice of
# WPT (see docs/decisions/0016-wpt-conformance.md). This keeps `make wpt` offline
# and hermetic (no network in CI, like every other target) while a corpus refresh
# stays an explicit, reviewable, re-run-the-report change — the same posture as the
# third_party/pdf vendoring (ADR 0001) and the deliberate dependency pins (ADR 0002).
#
# This script needs NETWORK and is run locally on purpose; it is never invoked by CI
# or `make test`. It does a sparse, blobless, single-commit fetch of only the
# in-scope directories plus WPT's shared /resources and /common, at the SHA recorded
# in testdata/wpt/WPT_VERSION, and rsyncs them into testdata/wpt/.
#
# Usage:
#   scripts/wpt-sync.sh                 # sync to the SHA in testdata/wpt/WPT_VERSION
#   WPT_SHA=<sha> scripts/wpt-sync.sh   # sync to an explicit SHA (updates WPT_VERSION)
set -euo pipefail

REPO="https://github.com/web-platform-tests/wpt.git"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/testdata/wpt"
VERSION_FILE="$DEST/WPT_VERSION"

# The vendored slice. Top-level dirs are taken whole; html/ is huge, so only its
# DOM/scripting subtrees are pulled. /resources and /common carry testharness.js,
# the reporter we override, and shared helpers. Keep this list in sync with the
# in-scope + partial + stub-only buckets in wpt/scope.json. Override for a partial
# refresh with WPT_DIRS="resources common encoding" scripts/wpt-sync.sh.
DIRS=(
  resources
  common
  encoding
  url
  hr-time
  domparsing
  FileAPI
  WebCryptoAPI
  custom-elements
  shadow-dom
  console
  streams
  dom
  html/dom
  html/semantics
  html/syntax
  html/webappapis
  fetch/api
  fetch/metadata
  xhr
  webmessaging
  cookies
  content-security-policy
  subresource-integrity
  referrer-policy
  IndexedDB
)
# WPT_DIRS overrides the list (space-separated) for a partial refresh during dev.
if [[ -n "${WPT_DIRS:-}" ]]; then
  read -r -a DIRS <<< "$WPT_DIRS"
fi

# Resolve the target SHA: explicit env override wins, else the pinned version file.
if [[ -n "${WPT_SHA:-}" ]]; then
  SHA="$WPT_SHA"
elif [[ -f "$VERSION_FILE" ]]; then
  SHA="$(tr -d '[:space:]' < "$VERSION_FILE")"
else
  echo "wpt-sync: no WPT_SHA set and no $VERSION_FILE; pass WPT_SHA=<sha>." >&2
  exit 1
fi
echo "wpt-sync: syncing ${#DIRS[@]} paths at $SHA" >&2

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

git -C "$TMP" init -q
git -C "$TMP" remote add origin "$REPO"
git -C "$TMP" config core.sparseCheckout true
git -C "$TMP" sparse-checkout init --no-cone
printf '%s\n' "${DIRS[@]}" | sed 's#^#/#' > "$TMP/.git/info/sparse-checkout"

# Single-commit, blob-filtered fetch of exactly the pinned SHA (GitHub allows
# fetching an arbitrary reachable SHA). --depth 1 avoids the whole history.
git -C "$TMP" fetch -q --depth 1 --filter=blob:none origin "$SHA"
git -C "$TMP" checkout -q FETCH_HEAD

# Mirror the sparse tree into testdata/wpt/, replacing the vendored dirs cleanly so
# upstream deletions are reflected. WPT_VERSION and this repo's own files are kept.
for d in "${DIRS[@]}"; do
  if [[ -d "$TMP/$d" ]]; then
    rm -rf "${DEST:?}/$d"
    mkdir -p "$(dirname "$DEST/$d")"
    cp -a "$TMP/$d" "$DEST/$d"
  else
    echo "wpt-sync: WARN upstream path missing: $d" >&2
  fi
done

printf '%s\n' "$SHA" > "$VERSION_FILE"
echo "wpt-sync: done → $DEST (WPT_VERSION=$SHA)" >&2
du -sh "$DEST" 2>/dev/null | sed 's/^/wpt-sync: size /' >&2
