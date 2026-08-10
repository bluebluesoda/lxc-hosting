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

# Local build: make it obvious WHICH branch will be compiled, and give the user
# a chance to abort — some people want a dev build but end up building stable.
if [[ "$BUILD_MODE" == "local" ]]; then
  BRANCH=$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "(no git repo / unknown)")
  echo
  echo "!! --local-build: compiling vpsmgr from this repository — branch: $BRANCH !!"
  echo "   Install starts in 10 seconds; Ctrl-C now if this is not the branch you intended."
  sleep 10
fi

# Reinstall after a non-purging uninstall: /etc/vpsmgr survives, adopt the
# previous users/domains/settings instead of starting over.
if [[ -f /etc/vpsmgr/config.yaml ]]; then
  echo "[install] found existing /etc/vpsmgr/config.yaml — adopting previous setup"
fi

echo "==> vpsmgr installer starting (panel binary mode: $BUILD_MODE)"
echo
echo "===== 00-ipv6-ask ====="
# shellcheck disable=SC1090
source "$ROOT/scripts/00-ipv6-ask.sh"
export VPSMGR_IPV6_SUBNET="${VPSMGR_IPV6_SUBNET:-}"

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
  echo "panel address:"
  vpsmgr panel-url
  echo "admin panel:   the password above was shown once by 'vpsmgr install'."
  echo "               forgot it? run: vpsmgr admin-passwd"
fi
echo "try: vpsmgr add alice"
echo "     ssh -p <base> root@<public-ip>"
