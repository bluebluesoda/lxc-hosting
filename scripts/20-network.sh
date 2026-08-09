#!/usr/bin/env bash
# 20-network.sh — sysctl + nftables basics.
set -uo pipefail
export PATH="$PATH:/snap/bin"

log(){ echo "[20] $*"; }

# ip_forward + BBR/fq TCP tuning. Written every run (idempotent).
cat > /etc/sysctl.d/99-vpsmgr.conf <<EOF
net.ipv4.ip_forward=1
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF
# IPv6 pass-through: forwarding must be on so the host relays container v6.
if [[ -n "${VPSMGR_IPV6_SUBNET:-}" ]]; then
  cat >> /etc/sysctl.d/99-vpsmgr.conf <<'EOF'
net.ipv6.conf.all.forwarding=1
net.ipv6.conf.default.forwarding=1
EOF
fi
log "wrote /etc/sysctl.d/99-vpsmgr.conf (ip_forward + bbr/fq${VPSMGR_IPV6_SUBNET:+ + ipv6 forwarding})"
SYSCTL_ARGS=(net.ipv4.ip_forward=1 net.core.default_qdisc=fq net.ipv4.tcp_congestion_control=bbr)
[[ -n "${VPSMGR_IPV6_SUBNET:-}" ]] && SYSCTL_ARGS+=(net.ipv6.conf.all.forwarding=1)
if ! sysctl -q -w "${SYSCTL_ARGS[@]}" 2>/dev/null; then
  log "warn: live apply of bbr/fq failed (kernel may not support bbr); config persisted and will apply on reboot"
fi
log "tcp congestion: $(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo 'n/a')"

if ! dpkg -s nftables >/dev/null 2>&1; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nftables
fi
log "nftables: $(nft --version 2>/dev/null | head -1)"

echo "[20] network ready"
