#!/usr/bin/env bash
# 40-panel.sh — install vpsmgr binary and initialize panel (config/cert/db/systemd).
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[40] $*"; }
die(){ echo "[40] error: $*" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ ! -x /usr/local/bin/vpsmgr ]]; then
  log "building vpsmgr from source..."
  bash "$ROOT/build.sh" || die "build failed"
  cp "$ROOT/bin/vpsmgr" /usr/local/bin/vpsmgr
  chmod 755 /usr/local/bin/vpsmgr
  log "installed /usr/local/bin/vpsmgr ($(/usr/local/bin/vpsmgr version))"
fi

log "running vpsmgr install (config/cert/db/nft/systemd)..."
/usr/local/bin/vpsmgr install

echo "[40] panel ready"
