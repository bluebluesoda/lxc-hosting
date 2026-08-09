#!/usr/bin/env bash
# 00-ipv6-ask.sh — ask whether to enable IPv6 pass-through and capture the
# global /64 prefix the user provides. The user is trusted; we only validate
# that the input is a syntactically valid global IPv6 prefix (/64 or shorter).
#
# Behavior:
#   - interactive: asks y/N, then prompts for the prefix (default = detect
#     from the host's own global address if it has one)
#   - non-interactive: VPSMGR_IPV6_SUBNET env var is used verbatim (validated)
#
# Writes nothing itself; on success it exports nothing but prints the prefix
# to stdout so the caller can capture it. Enabling is signalled by setting
# VPSMGR_IPV6_SUBNET for the rest of the install.
set -uo pipefail

log(){ echo "[ipv6] $*"; }
# NOTE: this script is `source`d by install.sh, so we must use `return` (not
# `exit`) at top level — `exit` would terminate the parent installer.
die(){ echo "[ipv6] error: $*" >&2; return 1; }

# validate_prefix: exit 0 if arg is a global IPv6 CIDR (/64 or shorter).
validate_prefix(){
  python3 - "$1" <<'PY'
import ipaddress, sys
p = sys.argv[1]
if "/" not in p:
    p += "/64"
try:
    n = ipaddress.IPv6Network(p, strict=False)
except Exception:
    sys.exit(1)
if n.prefixlen > 64:
    sys.exit(1)
a = n.network_address
if a.is_private or a.is_link_local or a.is_loopback or a.is_unspecified:
    sys.exit(1)
PY
}

# If the env var is already set, just validate and use it.
if [[ -n "${VPSMGR_IPV6_SUBNET:-}" ]]; then
  if validate_prefix "$VPSMGR_IPV6_SUBNET"; then
    log "IPv6 pass-through enabled with prefix $VPSMGR_IPV6_SUBNET (from env)"
    return 0
  else
    die "VPSMGR_IPV6_SUBNET='$VPSMGR_IPV6_SUBNET' is not a valid global IPv6 /64 (e.g. 2602:fada:6::/64)"
  fi
fi

# Non-interactive with no env var: IPv6 stays disabled.
if [[ ! -t 0 ]] && [[ -z "${FORCE_ASK:-}" ]]; then
  log "non-interactive install, no VPSMGR_IPV6_SUBNET set — IPv6 pass-through disabled"
  return 0
fi

echo
echo "============================================================"
echo " IPv6 pass-through  —  BETA / 实验性功能"
echo "------------------------------------------------------------"
echo " Each container gets its own public IPv6 address (no NAT)."
echo " Requires a globally routable IPv6 prefix from your provider."
echo " 每台小鸡将获得独立的公网 IPv6 地址（无 NAT）。"
echo " 需要服务商提供可路由的全球 IPv6 前缀。"
echo " Default: DISABLED. Only enable if you understand the risks."
echo " 默认不启用，请确认理解后再开启。"
echo "============================================================"
echo
read -r -p "Enable IPv6 pass-through? 启用 IPv6 直通? [y/N] " ans
case "${ans,,}" in
  y|yes)
    ;;
  *)
    log "IPv6 pass-through disabled / 未启用"
    return 0
    ;;
esac

# Suggest a candidate prefix from the host's own global address, if any.
CAND=""
EXT_IF=$(ip route show default 2>/dev/null | awk '{print $5; exit}')
GLOBAL=$(ip -6 -o addr show dev "$EXT_IF" scope global 2>/dev/null | awk '{print $4; exit}')
if [[ -n "$GLOBAL" ]]; then
  GADDR="${GLOBAL%%/*}"
  GLEN="${GLOBAL##*/}"
  GLEN="${GLEN:-64}"
  CAND=$(python3 -c 'import ipaddress,sys
a=ipaddress.IPv6Address(sys.argv[1])
plen=int(sys.argv[2])
n=ipaddress.IPv6Network((int(a), plen), strict=False)
print(n.network_address)' "$GADDR" "$GLEN")
  CAND="$CAND/$GLEN"
fi

if [[ -n "$CAND" ]]; then
  echo
  log "detected host global address: $GLOBAL"
  read -r -p "Global /64 prefix for containers [default: $CAND] (e.g. 2001:db8::/64): " PREFIX
  PREFIX="${PREFIX:-$CAND}"
else
  echo
  read -r -p "Global /64 prefix for containers (e.g. 2001:db8::/64, provided by your ISP): " PREFIX
fi

PREFIX="${PREFIX%$'\r'}"
# Normalize: accept "2602:fada:6::" (no length) as /64; always store the
# canonical CIDR form (with /64) so downstream parsing never fails.
PREFIX_NORM=$(python3 - "$PREFIX" <<'PY'
import ipaddress, sys
p = sys.argv[1]
if "/" not in p:
    p += "/64"
try:
    print(ipaddress.IPv6Network(p, strict=False))
except Exception:
    sys.exit(1)
PY
)
if validate_prefix "$PREFIX" && [[ -n "$PREFIX_NORM" ]]; then
  export VPSMGR_IPV6_SUBNET="$PREFIX_NORM"
  log "IPv6 pass-through enabled with prefix $PREFIX_NORM"
else
  die "invalid prefix '$PREFIX' — must be a global IPv6 /64 or shorter (e.g. 2602:fada:6::/64)"
fi
