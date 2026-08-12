#!/usr/bin/env bash
# 00-ipv4-ask.sh — ask whether to customize the container IPv4 subnet.
# The subnet is always 10.<n>.0.0/24 (gateway 10.<n>.0.1); only the second
# octet n is settable. Default 115. Fixed at install, immutable afterwards.
#
# Behavior:
#   - interactive: prints the fixed port scheme, asks for the octet (default 115)
#   - non-interactive: VPSMGR_IPV4_SUBNET env var used verbatim (validated),
#     else the default 10.115.0.0/24
#   - adoption (existing /etc/vpsmgr/config.yaml): keeps the recorded subnet
#
# Writes nothing itself; exports VPSMGR_IPV4_SUBNET for the rest of the install.
set -uo pipefail

log(){ echo "[ipv4] $*"; }
# NOTE: this script is `source`d by install.sh, so we must use `return` (not
# `exit`) at top level — `exit` would terminate the parent installer.
die(){ echo "[ipv4] error: $*" >&2; return 1; }

# --- dependency: python3 (octet/overlap validation below). Same lazy install
# as 00-ipv6-ask.sh.
if ! command -v python3 >/dev/null 2>&1; then
  log "python3 not found, installing..."
  if apt-get update -qq 2>/dev/null \
     && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3 >/dev/null 2>&1 \
     && command -v python3 >/dev/null 2>&1; then
    :
  else
    die "python3 required (apt install python3 failed)"
  fi
fi

# validate_octet: exit 0 if arg is an integer 1..254.
validate_octet(){
  python3 - "$1" <<'PY'
import sys
o = sys.argv[1]
if not o.isdigit() or not (1 <= int(o) <= 254):
    sys.exit(1)
PY
}

# overlaps_existing: exit 0 when 10.<octet>.0.0/24 does NOT overlap any existing
# IPv4 network on the host (routes or interface addresses). On overlap it prints
# the conflicting networks and exits 1.
overlaps_existing(){
  python3 - "$1" <<'PY'
import ipaddress, subprocess, sys
net = ipaddress.ip_network("10.%s.0.0/24" % sys.argv[1], strict=False)
out = []
for cmd in (["ip", "-4", "route", "show"], ["ip", "-4", "-o", "addr", "show"]):
    try:
        r = subprocess.run(cmd, capture_output=True, text=True).stdout
    except Exception:
        continue
    for line in r.splitlines():
        for tok in line.split():
            if "/" in tok:
                try:
                    n = ipaddress.ip_network(tok, strict=False)
                except Exception:
                    continue
                if n.version == 4 and net.overlaps(n):
                    out.append(str(n))
seen = sorted(set(out))
if seen:
    print(", ".join(seen))
    sys.exit(1)
PY
}

# --- IPv4 inbound forwarding policy (runs in every path, before the subnet
# early-returns below). Only meaningful when IPv6 pass-through is enabled: a
# v6-only box can drop IPv4 inbound entirely (containers keep NAT4 outbound).
# With IPv6 off, IPv4 forwarding is mandatory — otherwise containers would be
# unreachable.
if [[ -n "${VPSMGR_V4_FORWARD:-}" ]]; then
  case "$VPSMGR_V4_FORWARD" in
    1|0|true|false) ;;
    *) die "VPSMGR_V4_FORWARD must be 1/0 (got '$VPSMGR_V4_FORWARD')" ;;
  esac
else
  V4_FWD=""
  if [[ -f /etc/vpsmgr/config.yaml ]]; then
    V4_FWD=$(grep -E '^\s+v4_forward:' /etc/vpsmgr/config.yaml 2>/dev/null | awk -F': ' '{print $2}' | tr -d '"')
  fi
  if [[ -n "$V4_FWD" ]]; then
    export VPSMGR_V4_FORWARD="$V4_FWD"
  elif [[ -z "${VPSMGR_IPV6_SUBNET:-}" ]]; then
    export VPSMGR_V4_FORWARD=1
  elif [[ ! -t 0 ]]; then
    export VPSMGR_V4_FORWARD=1
  else
    echo
    read -r -p "保留 IPv4 入站转发（SSH + 端口段 + 域名）？ [Y/n] " ANS
    case "${ANS,,}" in
      n|no) export VPSMGR_V4_FORWARD=0 ;;
      *)    export VPSMGR_V4_FORWARD=1 ;;
    esac
  fi
fi
if [[ "${VPSMGR_V4_FORWARD:-1}" == "0" ]]; then
  log "IPv4 inbound forwarding DISABLED — containers IPv6-only (NAT4 outbound kept, traefik/domains off)"
else
  log "IPv4 inbound forwarding kept"
fi

# If the env var is already set, just validate and use it.
if [[ -n "${VPSMGR_IPV4_SUBNET:-}" ]]; then
  SUB="$VPSMGR_IPV4_SUBNET"
  if [[ "$SUB" =~ ^10\.([0-9]+)\.0\.0/24$ ]] && validate_octet "${BASH_REMATCH[1]}"; then
    log "container subnet: $SUB (from env)"
    export VPSMGR_IPV4_SUBNET="$SUB"
    return 0
  fi
  die "VPSMGR_IPV4_SUBNET='$SUB' must be 10.<n>.0.0/24 with n in 1..254 (e.g. 10.115.0.0/24)"
fi

# Reinstall after a non-purging uninstall: an existing config already holds the
# subnet — keep it instead of re-asking (a different answer would set the bridge
# to a prefix the config doesn't know and break container networking).
if [[ -f /etc/vpsmgr/config.yaml ]]; then
  EXISTING=$(grep -E '^\s+subnet:' /etc/vpsmgr/config.yaml 2>/dev/null | awk -F': ' '{print $2}' | tr -d '"')
  if [[ -n "$EXISTING" ]]; then
    log "existing config has subnet=$EXISTING — keeping it"
    export VPSMGR_IPV4_SUBNET="$EXISTING"
    return 0
  fi
fi

# Non-interactive with no env var: default subnet.
if [[ ! -t 0 ]] && [[ -z "${FORCE_ASK:-}" ]]; then
  log "non-interactive install — using default subnet 10.115.0.0/24"
  export VPSMGR_IPV4_SUBNET="10.115.0.0/24"
  return 0
fi

echo
echo "============================================================"
echo " 容器网络配置 / Container network"
echo "------------------------------------------------------------"
echo " 以下选项仅部分可自定义，无特殊需要请一路默认，设置后不可更改。"
echo " 设置后不可更改 / These are fixed after install."
echo " SSH 端口范围：30000-31999（每个容器随机分配一个）"
echo " 用户端口范围：10000-29999（每容器整百块，如 10700-10799）"
echo " 容器数量上限：200"
echo " 容器子网：10.<n>.0.0/24，默认 n=115"
if [[ -n "${VPSMGR_IPV6_SUBNET:-}" ]]; then
  echo "（启用 IPv6 时将询问是否保留 IPv4 入站；该选项可后用 vps v4-forward 切换）"
fi
echo "============================================================"
echo
read -r -p "第二段八位组 / Second octet of 10.x.0.0/24 (1-254) [default: 115]: " OCT
OCT="${OCT:-115}"
if ! validate_octet "$OCT"; then
  die "'$OCT' is not an integer in 1..254"
fi
if CONFLICT=$(overlaps_existing "$OCT"); then
  :
else
  echo
  log "warn: 10.$OCT.0.0/24 overlaps an existing host network ($CONFLICT)."
  read -r -p "      continue anyway? [y/N] " ANS
  case "${ANS,,}" in
    y|yes) ;;
    *) die "aborted — pick another octet" ;;
  esac
fi
export VPSMGR_IPV4_SUBNET="10.$OCT.0.0/24"
log "container subnet: 10.$OCT.0.0/24"
