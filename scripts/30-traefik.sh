#!/usr/bin/env bash
# 30-traefik.sh — install Traefik binary + static config + systemd service.
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[30] $*"; }
die(){ echo "[30] error: $*" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# fixed version + per-arch download URLs (no bundled binaries in the repo)
TRAEFIK_VERSION=3.3.5
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  TARCH=amd64 ;;
  aarch64) TARCH=arm64 ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
TRAEFIK_URL="https://github.com/traefik/traefik/releases/download/v${TRAEFIK_VERSION}/traefik_v${TRAEFIK_VERSION}_linux_${TARCH}.tar.gz"

if [[ ! -x /usr/local/bin/traefik ]]; then
  log "downloading traefik ${TRAEFIK_VERSION} (${TARCH})"
  log "  ${TRAEFIK_URL}"
  curl -fsSL -o /tmp/traefik.tar.gz "$TRAEFIK_URL" || die "traefik download failed"
  tar -xzf /tmp/traefik.tar.gz -C /tmp traefik
  cp /tmp/traefik /usr/local/bin/traefik
  chmod 755 /usr/local/bin/traefik
  log "installed /usr/local/bin/traefik"
fi
log "traefik version: $(/usr/local/bin/traefik version 2>/dev/null | awk '/Version:/{print $2; exit}')"

mkdir -p /etc/traefik/dynamic
if [[ ! -f /etc/traefik/traefik.yaml ]]; then
  cp "$ROOT/configs/traefik.yaml" /etc/traefik/traefik.yaml
  log "wrote /etc/traefik/traefik.yaml"
fi

if [[ ! -f /etc/systemd/system/traefik.service ]]; then
  cp "$ROOT/configs/systemd/traefik.service" /etc/systemd/system/traefik.service
  systemctl daemon-reload
  log "installed traefik.service"
fi

systemctl enable --now traefik >/dev/null 2>&1 || die "cannot start traefik"
sleep 1
if systemctl is-active traefik >/dev/null 2>&1; then
  log "traefik running"
else
  systemctl status traefik --no-pager | tail -5
  die "traefik failed to start"
fi

echo "[30] traefik ready"
