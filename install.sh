#!/usr/bin/env bash
# vpsmgr install.sh — main entry, idempotent, run as root.
# Usage:
#   ./install.sh                  # default: download latest prebuilt release binary (fallback: local build)
#   ./install.sh --local-build    # force local Go compilation of the panel binary
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$PWD"
export PATH="$PATH:/snap/bin"

BUILD_MODE=release
if [[ "${1:-}" == "--local-build" ]]; then
  BUILD_MODE=local
fi
export VPSMGR_BUILD_MODE="$BUILD_MODE"

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (sudo ./install.sh)" >&2
  exit 1
fi

echo "==> vpsmgr installer starting (panel binary mode: $BUILD_MODE)"
for step in 00-check 10-lxd 20-network 30-traefik 40-panel 50-image; do
  echo
  echo "===== $step ====="
  bash "$ROOT/scripts/$step.sh"
done

echo
echo "===== cleaning apt cache ====="
apt-get clean 2>/dev/null || true
rm -rf /var/lib/apt/lists/* 2>/dev/null || true

echo
echo "===== install complete ====="
if command -v vpsmgr >/dev/null 2>&1; then
  echo "panel address: $(vpsmgr panel-url)"
fi
echo "try: vpsmgr user add alice"
echo "     ssh -p <base> root@<public-ip>"
