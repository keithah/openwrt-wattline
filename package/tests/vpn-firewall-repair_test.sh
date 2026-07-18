#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
HELPER="$ROOT/package/wattlined/usr/lib/wattline/vpn-firewall-repair"
HOTPLUG="$ROOT/package/wattlined/etc/hotplug.d/iface/95-wattline"
TMP="${TMPDIR:-/tmp}/wattline-vpn-repair.$$"
CALLS="$TMP/calls"
export CALLS

trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"
: >"$CALLS"

fail() {
	printf 'vpn-firewall-repair_test: %s\n' "$*" >&2
	exit 1
}

assert_calls() {
	expected=$1
	actual="$(cat "$CALLS")"
	[ "$actual" = "$expected" ] || fail "expected calls [$expected], got [$actual]"
}

cat >"$TMP/bin/uci" <<'EOF'
#!/bin/sh
[ "${TAILSCALE_ENABLED:-1}" = 1 ] || exit 1
printf '1\n'
EOF
cat >"$TMP/bin/iptables" <<'EOF'
#!/bin/sh
printf 'iptables %s\n' "$*" >>"$CALLS"
case "$1" in
	-nL) [ "${TAILSCALE_CHAIN_PRESENT:-0}" = 1 ] ;;
	-C) [ "${TAILSCALE_JUMP_PRESENT:-0}" = 1 ] ;;
	*) exit 2 ;;
esac
EOF
cat >"$TMP/bin/logger" <<'EOF'
#!/bin/sh
printf 'logger %s\n' "$*" >>"$CALLS"
EOF
cat >"$TMP/bin/tailscale" <<'EOF'
#!/bin/sh
printf 'tailscale %s\n' "$*" >>"$CALLS"
if [ "${TAILSCALE_REPAIR_FAIL:-0}" = 1 ] && [ "$*" = 'set --netfilter-mode=on' ]; then
	exit 1
fi
EOF
cat >"$TMP/firewall-sync" <<'EOF'
#!/bin/sh
printf 'firewall-sync\n' >>"$CALLS"
EOF
chmod +x "$TMP/bin/uci" "$TMP/bin/iptables" "$TMP/bin/logger" \
	"$TMP/bin/tailscale" "$TMP/firewall-sync"

export PATH="$TMP/bin:/usr/bin:/bin"
export TAILSCALE="$TMP/bin/tailscale" IPTABLES="$TMP/bin/iptables"

[ -x "$HELPER" ] || fail "missing executable helper $HELPER"

TAILSCALE_ENABLED=0 "$HELPER"
assert_calls ''

: >"$CALLS"
TAILSCALE_CHAIN_PRESENT=1 TAILSCALE_JUMP_PRESENT=1 "$HELPER"
assert_calls "iptables -nL ts-input
iptables -C INPUT -j ts-input"

: >"$CALLS"
TAILSCALE_CHAIN_PRESENT=0 TAILSCALE_JUMP_PRESENT=0 "$HELPER"
assert_calls "iptables -nL ts-input
tailscale set --netfilter-mode=nodivert
tailscale set --netfilter-mode=on
logger -t wattline restored Tailscale firewall integration after OpenWrt reload"

: >"$CALLS"
TAILSCALE_CHAIN_PRESENT=1 TAILSCALE_JUMP_PRESENT=0 "$HELPER"
assert_calls "iptables -nL ts-input
iptables -C INPUT -j ts-input
tailscale set --netfilter-mode=nodivert
tailscale set --netfilter-mode=on
logger -t wattline restored Tailscale firewall integration after OpenWrt reload"

: >"$CALLS"
if TAILSCALE_REPAIR_FAIL=1 "$HELPER"; then
	fail 'failed Tailscale netfilter repair reported success'
fi
assert_calls "iptables -nL ts-input
tailscale set --netfilter-mode=nodivert
tailscale set --netfilter-mode=on
tailscale set --netfilter-mode=on
logger -t wattline failed to restore Tailscale firewall integration"

: >"$CALLS"
FIREWALL_SYNC="$TMP/firewall-sync" VPN_REPAIR="$HELPER" \
	ACTION=ifdown INTERFACE=speedify sh "$HOTPLUG"
assert_calls "firewall-sync
iptables -nL ts-input
tailscale set --netfilter-mode=nodivert
tailscale set --netfilter-mode=on
logger -t wattline restored Tailscale firewall integration after OpenWrt reload"

: >"$CALLS"
FIREWALL_SYNC="$TMP/firewall-sync" VPN_REPAIR="$HELPER" \
	ACTION=add INTERFACE=unrelated sh "$HOTPLUG"
assert_calls ''

echo 'VPN firewall repair tests passed'
