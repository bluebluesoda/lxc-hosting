#!/usr/bin/env bash
# vpsmgr install.sh — main entry, idempotent, run as root.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$PWD"
export PATH="$PATH:/snap/bin"

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (sudo ./install.sh)" >&2
  exit 1
fi

echo "==> vpsmgr installer starting"
for step in 00-check 10-lxd 20-network 30-traefik 40-panel 50-image; do
  echo
  echo "===== $step ====="
  bash "$ROOT/scripts/$step.sh"
done

echo
echo "===== install complete ====="
if command -v vpsmgr >/dev/null 2>&1; then
  echo "panel address: $(vpsmgr panel-url)"
fi
echo "try: vpsmgr user add alice"
echo "     ssh -p <base> root@<public-ip>"
