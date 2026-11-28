#!/usr/bin/env bash
# Rebuild the framework fixtures the eval gate renders (testdata/frameworks/built)
# from their sources (testdata/frameworks/src) with the real toolchains.
#
# Local-only, like wpt-sync: it needs network (npm) and Node, and is never run by
# CI or `make test`/`make eval` — those read the committed build output. Rebuild
# when bumping a framework version: edit the fixture's package.json, run this,
# review the diff, re-run `make eval`, and update testdata/frameworks/README.md.
#
# Usage: scripts/eval-fixtures.sh [fixture ...]   (default: all)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/testdata/frameworks/src"
built="$root/testdata/frameworks/built"

# Build order is fixed; webpack-esm-concat reuses ../webpack's sources.
all=(vite-react vite-vue webpack webpack-esm-concat next-app astro nuxt sveltekit angular solid)
fixtures=("$@")
if [ ${#fixtures[@]} -eq 0 ]; then
  fixtures=("${all[@]}")
fi

command -v node >/dev/null || { echo "eval-fixtures: node is required" >&2; exit 1; }
command -v npm >/dev/null || { echo "eval-fixtures: npm is required" >&2; exit 1; }
echo "eval-fixtures: node $(node --version), npm $(npm --version)"

# No telemetry, no update checks, no interactive prompts from any toolchain.
export NEXT_TELEMETRY_DISABLED=1 ASTRO_TELEMETRY_DISABLED=1 NUXT_TELEMETRY_DISABLED=1
export NG_CLI_ANALYTICS=false CI=1 npm_config_fund=false npm_config_audit=false
export npm_config_update_notifier=false

mkdir -p "$built"
for f in "${fixtures[@]}"; do
  [ -f "$src/$f/package.json" ] || { echo "eval-fixtures: unknown fixture $f" >&2; exit 1; }
  echo "== $f"
  (
    cd "$src/$f"
    rm -f .bundled-packages.json
    npm ci --no-audit --no-fund --loglevel=error
    npm run build --silent
  )
done

# Licence texts of everything bundled, next to each build (and a licence
# allowlist check: the build fails on anything copyleft or unlicensed).
node "$src/_shared/licenses.mjs"

echo "eval-fixtures: built size $(du -sh "$built" | cut -f1)"
