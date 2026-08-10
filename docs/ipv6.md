# IPv6 pass-through

Optional, **no NAT**: each container gets a global IPv6 address and the
outside can reach it directly. Enabled by setting `net.ipv6_subnet` (asked at
install; see [configuration.md](configuration.md)).

## Deterministic per-container address

An address is **computed on the fly from the username** — never stored, never
queried, stable across reinstalls:

```
address = [configured prefix][32-bit sha256(username)][0001]
                       bits 80-111            bits 112-127
```

- Example (`2602:fada:6::/64`, user `alice`): `2602:fada:6::2bd8:6c9:1`
- The fixed `0001` last block keeps every address off the all-zero
  subnet-router anycast.
- Because the 32-bit hash space is small, `vpsmgr add` refuses a name whose
  address collides with an existing user (hash collision).

## Supported prefixes

`/48` .. `/80` (an explicit length is required in `ipv6_subnet`).

| Prefix | Bridge uses | Notes |
|---|---|---|
| `/48` `/56` `/60` | **first /64 of the prefix** | LXD's dnsmasq rejects non-/64 networks, and every deterministic address falls inside the first /64 anyway (bits `[prefixlen:79]` are zero-filled). |
| `/64` | the /64 itself | |
| `/80` | the /80 itself | Common provider slice (e.g. AWS ENI /80). |

The bridge prefix length is clamped with `min(ones, 64)`-equivalent logic
(`bridgePrefixLen`); both the installer preseed (`10-lxd.sh`) and
`SetupIPv6Bridge` (run by `vpsmgr install` and on boot) apply it.

## Bridge setup (`SetupIPv6Bridge`)

The bridge (`lxdbr0`) gets:

```
ipv6.address = <gw>/<len>     # len = bridgePrefixLen(ones)
ipv6.nat = false
ipv6.routing = true
ipv6.dhcp.stateful = true
```

The gateway is the first free address in the prefix (`net+1`, `net+2`, ...)
that the host can see is already taken:

1. addresses assigned to the host's external interface (e.g. the host itself
   holds `::1` on a /80 slice),
2. the upstream default gateway(s) (a global gateway inside the prefix, e.g.
   an ISP's `::1`, must never be claimed — the host would answer for it and
   break its own outbound routing),
3. any address in the NDP neighbor table on the external interface.

The conflicting prefix route LXD auto-creates on the bridge is deleted (eth0
keeps the authoritative route), and IPv6 forwarding is enabled.

## Per-container wiring

For each container:

- `lxc config device override eth0` sets a static `ipv6.address` (IPv4 and
  IPv6 in one override call).
- The host adds an exact `/128` route via `lxdbr0` and a `proxy_ndp` entry for
  the address on the external interface, so upstream neighbor solicitations
  are answered by the host and traffic reaches the container.

Wiring is applied on `add`/`reinstall`, and re-applied for all containers at
boot by `vpsmgr-ipv6.service` / `vpsmgr ipv6-reapply` and by
`vpsmgr install`. Deleted with the container on `del`.

## Installer flow

- `00-ipv6-ask.sh` — asks whether to enable IPv6 and captures the prefix.
  The prefix length is **required** (no silent `/64` default). On reinstall it
  reuses an existing config's `ipv6_subnet` instead of re-asking.
- `10-lxd.sh` — puts the (clamped) prefix on `lxdbr0` at `lxd init` time.
- `20-network.sh` — enables IPv6 forwarding.
- `50-image.sh` / `vpsmgr install` — nothing IPv6-specific.
- `check-ipv6-support.sh` — probe before install: reports the host's global
  addresses, auto-measures the subnet size (prefers the on-link routed block,
  e.g. AWS's /80, over the address's own configured length), and verifies from
  the outside (Globalping, free) that the provider actually routes the whole
  prefix to the host.

## Uninstall cleanup

`uninstall.sh` reads the prefix from the config before removal, then: removes
`proxy_ndp` entries and `/128` routes matching the prefix, resets `lxdbr0`
IPv6 to disabled, and restores forwarding sysctls.

## Future: per-container /112 blocks (`lab` branch)

The `lab` branch (`80a2dd0`) experiments with giving each container a whole
`/112` block (16 host bits) via `ipv6.routes` + `ndppd`, so a container can
bind arbitrary addresses in its block. The primary address is byte-identical
to the current scheme, so it could be merged later without changing existing
container addresses. Not planned for the mainline yet.
