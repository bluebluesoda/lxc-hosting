#!/usr/bin/env bash
# uninstall.sh — remove vpsmgr. --purge also removes containers and storage.
set -uo pipefail
export PATH="$PATH:/snap/bin"

PURGE=0
if [[ "${1:-}" == "--purge" ]]; then PURGE=1; fi
log(){ echo "[un] $*"; }

log "stopping services..."
for svc in vpsmgr-panel vpsmgr-nft traefik; do
  systemctl disable --now "$svc.service" >/dev/null 2>&1 || true
done
systemctl daemon-reload >/dev/null 2>&1 || true

log "removing files..."
rm -f /usr/local/bin/vpsmgr /usr/local/bin/traefik
rm -f /etc/systemd/system/vpsmgr-panel.service /etc/systemd/system/vpsmgr-nft.service /etc/systemd/system/traefik.service
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
