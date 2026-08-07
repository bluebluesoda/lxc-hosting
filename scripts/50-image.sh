#!/usr/bin/env bash
# 50-image.sh — pre-pull Debian 13 and build a local "Debian 13 + sshd" image.
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[50] $*"; }

lxc info >/dev/null 2>&1 || { echo "[50] error: LXD not ready" >&2; exit 1; }

# --- base image ---
if lxc image show vpsmgr-debian-13 >/dev/null 2>&1; then
  log "base image vpsmgr-debian-13 already present"
else
  log "pulling images:debian/13 (fallback images:debian/trixie)..."
  if ! lxc image copy images:debian/13 local: --alias vpsmgr-debian-13; then
    log "  debian/13 alias failed, trying debian/trixie"
    lxc image copy images:debian/trixie local: --alias vpsmgr-debian-13 \
      || log "  warn: image pull failed — add-user needs network and will retry"
  fi
fi

# --- prebuilt sshd image ---
if lxc image show vpsmgr/debian-sshd >/dev/null 2>&1; then
  log "sshd image vpsmgr/debian-sshd already present"
  exit 0
fi

if ! lxc image show vpsmgr-debian-13 >/dev/null 2>&1; then
  log "  warn: base image unavailable, skipping sshd image build (add-user will install sshd on the fly)"
  exit 0
fi

log "building sshd image (this takes a few minutes)..."
NAME=tmp-sshd-builder
lxc delete --force "$NAME" >/dev/null 2>&1 || true
if lxc launch vpsmgr-debian-13 "$NAME"; then
  # wait until usable
  for i in $(seq 1 60); do
    if lxc exec "$NAME" -- /bin/true >/dev/null 2>&1; then break; fi
    sleep 2
  done
  if lxc exec "$NAME" -- sh -c 'export DEBIAN_FRONTEND=noninteractive
apt-get update -qq && apt-get install -y -qq openssh-server
mkdir -p /etc/ssh/sshd_config.d
printf "PermitRootLogin yes\nPasswordAuthentication yes\n" > /etc/ssh/sshd_config.d/99-vpsmgr.conf
systemctl enable ssh'; then
    lxc stop "$NAME" --timeout=30 || true
    lxc publish "$NAME" --alias vpsmgr/debian-sshd
    lxc delete --force "$NAME" || true
    log "sshd image published: vpsmgr/debian-sshd"
  else
    log "  warn: sshd install in builder failed; add-user will install sshd on the fly"
    lxc delete --force "$NAME" >/dev/null 2>&1 || true
  fi
else
  log "  warn: could not launch builder; add-user will install sshd on the fly"
fi

echo "[50] image ready"
