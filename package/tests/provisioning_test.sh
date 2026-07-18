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
	show)
		key="$1"
		[ ! -e "${FAIL_UCI_SHOW:-}" ] || exit 1
		awk -v key="$key" 'index($0, key "=") == 1 || index($0, key ".") == 1 { print; found = 1 } END { exit !found }' "$STATE"
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

# The lifecycle contract: sync before spawning, restart for any daemon main
# setting change, but retain SIGHUP for rule-only reloads.
sync_line="$(grep -n '/usr/lib/wattline/firewall-sync' "$INIT" | head -n 1 | cut -d: -f1)"
open_line="$(grep -n 'procd_open_instance' "$INIT" | head -n 1 | cut -d: -f1)"
[ "$sync_line" -lt "$open_line" ]
grep -Fq '"$SERVICE_SCRIPT" restart' "$INIT"
grep -Fq 'kill -HUP' "$INIT"
grep -Fq 'procd_set_param respawn' "$INIT"
grep -Fq 'procd_set_param stdout 1' "$INIT"
grep -Fq 'procd_set_param stderr 1' "$INIT"

FIREWALL_SYNC="$TMP/firewall-sync"
SERVICE_SCRIPT="$TMP/service"
PGREP="$TMP/pgrep"
RUNTIME_DIR="$TMP/run"
DAEMON_STATE="$RUNTIME_DIR/daemon-state"
export FIREWALL_SYNC SERVICE_SCRIPT PGREP RUNTIME_DIR DAEMON_STATE
cat > "$FIREWALL_SYNC" <<'EOF'
#!/bin/sh
printf 'sync\n' >> "$CALLS"
EOF
cat > "$SERVICE_SCRIPT" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
EOF
cat > "$PGREP" <<'EOF'
#!/bin/sh
printf 'pgrep\n' >> "$CALLS"
exit 1
EOF
chmod +x "$FIREWALL_SYNC" "$SERVICE_SCRIPT" "$PGREP"

# rc.common normally sources this file; sourcing lets the harness call its
# lifecycle functions with harmless injected commands.
. "$INIT"
procd_open_instance() { printf 'procd-open\n' >> "$CALLS"; }
procd_set_param() { :; }
procd_close_instance() { :; }
save_daemon_state
[ "$(ls -ld "$RUNTIME_DIR" | awk '{print $1}')" = drwx------ ]
[ "$(ls -l "$DAEMON_STATE" | awk '{print $1}')" = -rw------- ]

for assignment in \
	wattline.main.advanced=1 \
	wattline.main.mdns_enabled=0 \
	wattline.main.pairing_ttl=2m \
	wattline.main.pairing_always_on=1 \
	wattline.main.token_store=/etc/wattline/alternate.json \
	wattline.main.wan_access=1 \
	wattline.main.port=9222
do
	restart_count="$(grep -Fc 'restart' "$CALLS" || true)"
	printf '%s\n' "$assignment" >> "$STATE"
	reload_service
	[ "$(grep -Fc 'restart' "$CALLS")" -eq "$((restart_count + 1))" ]
	save_daemon_state
done

restart_count="$(grep -Fc 'restart' "$CALLS")"
printf '%s\n' 'wattline.rule.enabled=1' >> "$STATE"
reload_service
[ "$(grep -Fc 'restart' "$CALLS")" -eq "$restart_count" ]

# UCI read failures cannot produce an empty-but-valid daemon fingerprint.
# Preserve the last good state and do not proceed to procd, restart, pgrep, or
# HUP when startup/reload cannot read wattline.main.
cp "$DAEMON_STATE" "$TMP/daemon-state.good"
FAIL_UCI_SHOW="$TMP/fail-uci-show"
export FAIL_UCI_SHOW
: > "$FAIL_UCI_SHOW"
before_procd="$(grep -Fc 'procd-open' "$CALLS" || true)"
if start_service; then
	echo "provisioning_test: startup accepted unreadable UCI" >&2
	exit 1
fi
[ "$(grep -Fc 'procd-open' "$CALLS" || true)" -eq "$before_procd" ]
cmp -s "$DAEMON_STATE" "$TMP/daemon-state.good"
if find "$RUNTIME_DIR" -name 'daemon-state.new.*' | grep -q .; then
	echo "provisioning_test: failed snapshot left temporary state" >&2
	exit 1
fi

before_restart="$(grep -Fc 'restart' "$CALLS" || true)"
before_pgrep="$(grep -Fc 'pgrep' "$CALLS" || true)"
if reload_service; then
	echo "provisioning_test: reload accepted unreadable UCI" >&2
	exit 1
fi
[ "$(grep -Fc 'restart' "$CALLS" || true)" -eq "$before_restart" ]
[ "$(grep -Fc 'pgrep' "$CALLS" || true)" -eq "$before_pgrep" ]
cmp -s "$DAEMON_STATE" "$TMP/daemon-state.good"
rm -f "$FAIL_UCI_SHOW"

echo "provisioning tests passed"
