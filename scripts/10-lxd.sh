#!/usr/bin/env bash
# 10-lxd.sh — install LXD snap, storage pool, bridge.
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[10] $*"; }
die(){ echo "[10] error: $*" >&2; exit 1; }

# --- ensure LXD snap 5.21/stable ---
if snap list lxd >/dev/null 2>&1; then
  log "lxd snap already installed: $(snap list lxd | awk 'NR==2{print $2}')"
else
  log "installing lxd snap (5.21/stable)..."
  snap install lxd --channel=5.21/stable || die "snap install lxd failed"
fi
log "waiting for LXD daemon..."
lxd waitready --timeout=120 || die "lxd daemon not ready"

# --- zfs kernel module ---
if ! command -v zpool >/dev/null 2>&1; then
  log "installing zfsutils-linux (kernel module)"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq zfsutils-linux
fi

# --- storage pool ---
POOL=vpsmgr
POOL_EXISTS=0
if lxc storage show "$POOL" >/dev/null 2>&1; then
  POOL_EXISTS=1
  log "storage pool '$POOL' exists ($(lxc storage show "$POOL" | awk -F': ' '/driver:/{print $2}'))"
fi

# find a spare whole-disk block device (no partitions, not the root disk, unmounted)
find_spare_disk(){
  ROOT_DISK=$(lsblk -rno NAME,MOUNTPOINTS | awk '$2=="/"{print $1; exit}' | sed 's/[0-9]*$//')
  for d in $(lsblk -rno NAME,TYPE | awk '$2=="disk"{print $1}'); do
    [[ "$d" == "$ROOT_DISK" ]] && continue
    [[ -b "/dev/$d" ]] || continue
    NCHILD=$(lsblk -rno NAME "$d" | tail -n +2 | wc -l)
    [[ "$NCHILD" -gt 0 ]] && continue
    grep -q "/dev/$d" /proc/mounts && continue
    echo "/dev/$d"; return 0
  done
  return 1
}

if [[ $POOL_EXISTS -eq 0 ]]; then
  SPARE=$(find_spare_disk || true)
  if [[ -n "$SPARE" ]]; then
    log "creating zfs pool '$POOL' on spare block device $SPARE"
    zpool create -f "$POOL" "$SPARE" && log "zpool created from $SPARE" \
      || log "  warn: zpool create on $SPARE failed, falling back to loop file"
  fi
  if ! zpool list "$POOL" >/dev/null 2>&1; then
    # loop-file pool: 80% of free space
    POOL_DIR=/var/lib/vpsmgr
    mkdir -p "$POOL_DIR"
    FREE_KB=$(df -k --output=avail "$POOL_DIR" | tail -1 | tr -d ' ')
    SIZE_MB=$(( FREE_KB / 1024 * 80 / 100 ))
    log "creating loop-file zfs pool '$POOL' (~${SIZE_MB} MiB = 80% of free)"
    IMG="$POOL_DIR/pool.img"
    fallocate -l "${SIZE_MB}M" "$IMG" || truncate -s "${SIZE_MB}M" "$IMG"
    if zpool create -f "$POOL" "$IMG"; then
      log "zpool created from loop file $IMG"
    else
      log "  warn: loop zfs failed, falling back to dir backend (quotas disabled)"
      rm -f "$IMG"
      DRIVER=dir
    fi
  fi
fi

# --- lxd init (preseed) ---
if ! lxc storage show "$POOL" >/dev/null 2>&1 || ! lxc network show lxdbr0 >/dev/null 2>&1; then
  DRIVER=${DRIVER:-zfs}
  PRESEED=/tmp/vpsmgr-preseed.yaml
  if [[ "$DRIVER" == "zfs" ]]; then
    SRC_LINE="    source: \"$POOL\""
  else
    SRC_LINE=""
  fi
  cat > "$PRESEED" <<EOF
config: {}
networks:
- config:
    ipv4.address: 10.42.0.1/24
    ipv4.nat: "true"
    ipv6.address: none
    ipv6.nat: "false"
  description: ""
  name: lxdbr0
  type: bridge
storage_pools:
- config:
$SRC_LINE
  description: ""
  name: $POOL
  driver: $DRIVER
profiles:
- config: {}
  description: Default LXD profile
  devices:
    eth0:
      name: eth0
      nictype: bridged
      parent: lxdbr0
      type: nic
    root:
      path: /
      pool: $POOL
      type: disk
  name: default
cluster: null
EOF
  log "running lxd init --preseed (driver=$DRIVER, subnet 10.42.0.1/24)"
  if ! lxd init --preseed < "$PRESEED"; then
    log "preseed failed — checking partial state"
    lxc storage show "$POOL" >/dev/null 2>&1 || lxc storage create "$POOL" "$DRIVER" source="$POOL"
    lxc network show lxdbr0 >/dev/null 2>&1 || lxc network create lxdbr0 ipv4.address=10.42.0.1/24 ipv4.nat=true ipv6.address=none
  fi
  # ensure default profile devices point at our pool/bridge
  lxc profile set default root.pool "$POOL" 2>/dev/null || true
  lxc profile device set default root pool "$POOL" 2>/dev/null || true
  lxc profile device set default eth0 parent lxdbr0 2>/dev/null || true
else
  log "LXD already initialized (pool+network present)"
fi

DRIVER_NOW=$(lxc storage show "$POOL" | awk -F': ' '/driver:/{print $2}')
log "storage backend: $DRIVER_NOW"
if [[ "$DRIVER_NOW" == "dir" ]]; then
  log "  warn: dir backend — disk quotas NOT enforced"
fi

echo "[10] lxd ready"
