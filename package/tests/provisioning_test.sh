#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/package/wattlined/etc/uci-defaults/99-wattline"
INIT="$ROOT/package/wattlined/etc/init.d/wattlined"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"
STATE="$TMP/uci"
CALLS="$TMP/calls"
export STATE CALLS

cat > "$TMP/bin/wattlined" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
EOF
cat > "$TMP/bin/uci" <<'EOF'
#!/bin/sh
set -eu
[ "${1:-}" = -q ] && shift
command="$1"
shift
case "$command" in
	get)
		key="$1"
		value="$(sed -n "s|^${key}=||p" "$STATE" | tail -n 1)"
		[ -n "$value" ] || exit 1
		printf '%s\n' "$value"
		;;
	set|add_list)
		assignment="$1"
		printf '%s\n' "$assignment" >> "$STATE"
		;;
	commit) printf 'commit %s\n' "$1" >> "$CALLS" ;;
	*) exit 2 ;;
esac
EOF
chmod +x "$TMP/bin/wattlined" "$TMP/bin/uci"
export PATH="$TMP/bin:$PATH"
export WATTLINE_BIN="$TMP/bin/wattlined"

cat > "$STATE" <<'EOF'
wattline.main.port=9123
wattline.main.http_enabled=0
wattline.main.token=keep-bootstrap-secret
EOF

sh "$SCRIPT"
grep -Fqx -- '-init -config /etc/config/wattline' "$CALLS"
grep -Fqx 'wattline.main.port=9123' "$STATE"
[ "$(grep -Fc 'wattline.main.port=' "$STATE")" -eq 1 ]
grep -Fqx 'wattline.main.token=keep-bootstrap-secret' "$STATE"
grep -Fqx 'wattline.main.tls_key=/etc/wattline/tls/server.key' "$STATE"
grep -Fqx 'wattline.main.wan_access=0' "$STATE"
grep -Fqx 'wattline.main.mdns_interface=br-lan' "$STATE"
[ "$(grep -Fc 'commit wattline' "$CALLS")" -eq 1 ]

cp "$STATE" "$TMP/before"
sh "$SCRIPT"
cmp -s "$STATE" "$TMP/before"
[ "$(grep -Fc 'commit wattline' "$CALLS")" -eq 1 ]
[ "$(grep -Fc -- '-init -config /etc/config/wattline' "$CALLS")" -eq 2 ]

# The lifecycle contract: sync before spawning, restart for listener identity
# changes, but retain SIGHUP for rule-only reloads.
sync_line="$(grep -n '/usr/lib/wattline/firewall-sync' "$INIT" | head -n 1 | cut -d: -f1)"
open_line="$(grep -n 'procd_open_instance' "$INIT" | head -n 1 | cut -d: -f1)"
[ "$sync_line" -lt "$open_line" ]
grep -Fq '/etc/init.d/wattlined restart' "$INIT"
grep -Fq 'kill -HUP' "$INIT"
grep -Fq 'procd_set_param respawn' "$INIT"
grep -Fq 'procd_set_param stdout 1' "$INIT"
grep -Fq 'procd_set_param stderr 1' "$INIT"

echo "provisioning tests passed"
