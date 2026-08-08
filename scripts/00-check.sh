#!/usr/bin/env bash
# 00-check.sh — environment sanity checks.
set -uo pipefail

log(){ echo "[00] $*"; }
die(){ echo "[00] error: $*" >&2; exit 1; }

if [[ $EUID -ne 0 ]]; then die "must run as root"; fi

# --- distro ---
if [[ ! -f /etc/os-release ]]; then die "cannot find /etc/os-release"; fi
. /etc/os-release
if [[ "${ID:-}" != "ubuntu" ]] || [[ "${VERSION_ID:-}" != "24.04" ]]; then
  die "this installer targets Ubuntu 24.04 (got ${PRETTY_NAME:-unknown})"
fi

# --- virtualization (require physical or KVM) ---
if command -v systemd-detect-virt >/dev/null 2>&1; then
  VIRT=$(systemd-detect-virt)
  case "$VIRT" in
    openvz|lxc|lxc-libvirt|wsl|container|vm-other|podman|docker)
      die "unsupported environment: '$VIRT'. Need a physical machine or KVM VM."
      ;;
  esac
  log "virtualization: ${VIRT:-none (physical)}"
fi

# --- architecture ---
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  GOARCH=amd64 ;;
  aarch64) GOARCH=arm64 ;;
  *) die "unsupported architecture: $ARCH (only amd64 / arm64 are supported)" ;;
esac
log "architecture: $ARCH ($GOARCH)"

# --- hardware (warn only) ---
CPUS=$(nproc 2>/dev/null || echo 0)
MEM_KB=$(awk '/MemTotal/{print $2}' /proc/meminfo)
MEM_GB=$(( MEM_KB / 1024 / 1024 ))
log "cpu: ${CPUS} cores, mem: ${MEM_GB} GiB"
[[ $CPUS -lt 2 ]] && log "  warn: < 2 CPU may be slow"
[[ $MEM_GB -lt 2 ]] && log "  warn: < 2 GiB RAM is tight"

# --- disk ---
FREE_KB=$(df -k --output=avail / | tail -1 | tr -d ' ')
log "free disk on /: $(( FREE_KB / 1024 )) MiB"
[[ $FREE_KB -lt 5*1024*1024 ]] && die "need at least 5 GiB free on /"

# --- packages ---
for p in snapd nftables zstd; do
  if ! dpkg -s "$p" >/dev/null 2>&1; then
    log "installing $p"
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$p"
  fi
done

# --- Go toolchain (only needed for local build; installed lazily by 40-panel.sh) ---
if command -v go >/dev/null 2>&1; then
  log "go: $(go version 2>/dev/null | awk '{print $3}')"
fi

# --- LXD snap ---
if ! snap list lxd >/dev/null 2>&1; then
  log "lxd snap not installed yet (installed by 10-lxd.sh)"
fi

# --- detect public ip / ext iface ---
EXT_IF=$(ip route show default | awk '{print $5; exit}')
PUB_IP=""
if [[ -n "$EXT_IF" ]]; then
  PUB_IP=$(ip -4 -o addr show dev "$EXT_IF" scope global | awk '{print $4}' | cut -d/ -f1 | head -1)
fi
if [[ -z "$PUB_IP" ]]; then
  PUB_IP=$(hostname -I | awk '{print $1}')
  log "  warn: no public IP detected on $EXT_IF, using $PUB_IP (private) as fallback"
fi
log "public/panel IP: $PUB_IP  (ext iface: ${EXT_IF:-auto})"

# --- network reachability (warn only) ---
if ! curl -sI --max-time 8 https://images.linuxcontainers.org >/dev/null 2>&1; then
  log "  warn: cannot reach images.linuxcontainers.org — LXD image pull will fail unless cached"
fi

echo "[00] checks passed"
