#!/usr/bin/env bash
# SnapVault launcher — builds (if needed) and starts the local web UI.
# Safe to run from any directory; it always uses the repo it lives in.
set -euo pipefail

# Resolve the directory this script lives in, following symlinks.
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do
  DIR="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE"
done
REPO="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"

cd "$REPO"

BIN="$REPO/snapvault"
# Rebuild if the binary is missing or any Go source / embedded web asset is newer.
if [ ! -x "$BIN" ] || [ -n "$(find "$REPO" \( -name '*.go' -o -path "$REPO/web/*" \) -newer "$BIN" -print -quit 2>/dev/null)" ]; then
  echo "🔨 Building SnapVault…"
  go build -o "$BIN" .
fi

# Keep config in the repo so saved NAS shares persist regardless of where you launch from.
CONFIG="$REPO/config.yaml"

# Pass through any extra flags (e.g. -addr 0.0.0.0:8080, -no-open).
exec "$BIN" -serve -config "$CONFIG" "$@"
