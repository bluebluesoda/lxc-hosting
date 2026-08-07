#!/usr/bin/env bash
# 20-network.sh — sysctl + nftables basics.
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[20] $*"; }

if [[ ! -f /etc/sysctl.d/99-vpsmgr.conf ]]; then
  echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-vpsmgr.conf
  log "wrote /etc/sysctl.d/99-vpsmgr.conf"
fi
sysctl -q -w net.ipv4.ip_forward=1
log "ip_forward=1"

if ! dpkg -s nftables >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nftables
fi
log "nftables: $(nft --version 2>/dev/null | head -1)"

echo "[20] network ready"
