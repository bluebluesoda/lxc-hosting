#!/usr/bin/env bash
# uninstall.sh — remove vpsmgr. --purge also removes containers and storage.
set -uo pipefail
export PATH="$PATH:/snap/bin"

PURGE=0
if [[ "${1:-}" == "--purge" ]]; then PURGE=1; fi
log(){ echo "[un] $*"; }

log "stopping services..."
for svc in vpsmgr-panel vpsmgr-nft vpsmgr-ipv6 traefik; do
  systemctl disable --now "$svc.service" >/dev/null 2>&1 || true
done
systemctl daemon-reload >/dev/null 2>&1 || true

# --- IPv6 pass-through cleanup (before removing config, which holds the prefix) ---
V6SUBNET=""
if [[ -f /etc/vpsmgr/config.yaml ]]; then
  V6SUBNET=$(grep -E '^\s+ipv6_subnet:' /etc/vpsmgr/config.yaml 2>/dev/null | awk -F': ' '{print $2}' | tr -d '"')
fi
if [[ -n "$V6SUBNET" ]]; then
  log "cleaning IPv6 pass-through ($V6SUBNET)..."
  # remove proxy_ndp entries on the ext iface for the prefix
  EXT_IF=$(ip route show default 2>/dev/null | awk '{print $5; exit}')
  if [[ -n "$EXT_IF" ]]; then
    ip -6 neigh show proxy dev "$EXT_IF" 2>/dev/null | awk '{print $1}' | while read -r a; do
      case "$a" in
        ${V6SUBNET%%/*}*) ip -6 neigh del proxy "$a" dev "$EXT_IF" 2>/dev/null || true ;;
      esac
    done
    sysctl -w net.ipv6.conf."$EXT_IF".proxy_ndp=0 >/dev/null 2>&1 || true
  fi
  # remove per-container /128 routes via lxdbr0 for the prefix
  ip -6 route show dev lxdbr0 2>/dev/null | awk '{print $1}' | while read -r a; do
    case "$a" in
      ${V6SUBNET%%/*}*) ip -6 route del "$a" dev lxdbr0 2>/dev/null || true ;;
    esac
  done
  # restore lxdbr0 IPv6 to disabled (matches vpsmgr default)
  if command -v lxc >/dev/null 2>&1 && lxc network show lxdbr0 >/dev/null 2>&1; then
    lxc network set lxdbr0 ipv6.address none 2>/dev/null || true
    lxc network set lxdbr0 ipv6.nat false 2>/dev/null || true
    lxc network set lxdbr0 ipv6.routing false 2>/dev/null || true
    lxc network set lxdbr0 ipv6.dhcp.stateful false 2>/dev/null || true
  fi
  # live sysctls back to defaults
  sysctl -w net.ipv6.conf.all.forwarding=0 net.ipv6.conf.default.forwarding=0 >/dev/null 2>&1 || true
fi

log "removing files..."
rm -f /usr/local/bin/vpsmgr /usr/local/bin/traefik
rm -f /etc/systemd/system/vpsmgr-panel.service /etc/systemd/system/vpsmgr-nft.service /etc/systemd/system/vpsmgr-ipv6.service /etc/systemd/system/traefik.service
rm -rf /etc/vpsmgr /etc/traefik
rm -f /etc/sysctl.d/99-vpsmgr.conf
nft delete table inet vpsmgr 2>/dev/null || true

if [[ $PURGE -eq 1 ]]; then
  log "purging LXD instances..."
  for c in $(lxc list --format=csv -c n 2>/dev/null); do
    log "  deleting container $c"
    lxc delete --force "$c" >/dev/null 2>&1 || true
  done
  log "removing storage pool..."
  lxc storage delete vpsmgr >/dev/null 2>&1 || true
  if command -v zpool >/dev/null 2>&1 && zpool list vpsmgr >/dev/null 2>&1; then
    zpool destroy -f vpsmgr >/dev/null 2>&1 || true
  fi
  rm -rf /var/lib/vpsmgr
  log "removing lxd snap..."
  snap remove lxd --purge >/dev/null 2>&1 || true
fi

log "done. use ./install.sh to reinstall."
