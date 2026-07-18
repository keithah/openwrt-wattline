#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
HELPER="$ROOT/package/wattlined/usr/lib/wattline/firewall-sync"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

mkdir -p "$TMP/bin"
STATE="$TMP/uci"
LOG="$TMP/logger"
RELOADS="$TMP/reloads"
APPLY_MARKER="$TMP/firewall.pending"
export STATE LOG RELOADS APPLY_MARKER

cat > "$TMP/bin/uci" <<'EOF'
#!/bin/sh
set -eu
[ "${1:-}" = "-q" ] && shift
command="$1"
shift
case "$command" in
	get)
		key="$1"
		line="$(sed -n "s|^${key}=||p" "$STATE" 2>/dev/null | tail -n 1)"
		[ -n "$line" ] || exit 1
		printf '%s\n' "$line"
		;;
	show)
		key="$1"
		awk -v key="$key" 'index($0, key "=") == 1 || index($0, key ".") == 1 { print; found = 1 } END { exit !found }' "$STATE"
		;;
	set)
		assignment="$1"
		key="${assignment%%=*}"
		value="${assignment#*=}"
		tmp="${STATE}.tmp"
		{ sed "\|^${key}=|d" "$STATE" 2>/dev/null || true; printf '%s=%s\n' "$key" "$value"; } > "$tmp"
		mv "$tmp" "$STATE"
		;;
	delete)
		key="$1"
		tmp="${STATE}.tmp"
		awk -v key="$key" 'index($0, key "=") != 1 && index($0, key ".") != 1' "$STATE" > "$tmp"
		mv "$tmp" "$STATE"
		;;
	commit)
		printf 'commit %s\n' "$1" >> "$STATE.commands"
		;;
	*) exit 2 ;;
esac
EOF
cat > "$TMP/bin/logger" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$LOG"
EOF
cat > "$TMP/firewall" <<'EOF'
#!/bin/sh
[ "$1" = reload ]
printf 'reload\n' >> "$RELOADS"
[ ! -e "${FAIL_RELOAD:-}" ]
EOF
chmod +x "$TMP/bin/uci" "$TMP/bin/logger" "$TMP/firewall"
export PATH="$TMP/bin:$PATH"
export FIREWALL_INIT="$TMP/firewall"

fail() {
	echo "firewall-sync_test: $*" >&2
	exit 1
}

assert_line() {
	grep -Fqx "$1" "$STATE" || fail "missing state: $1"
}

assert_absent() {
	if grep -Eq "^$1(=|\.)" "$STATE"; then
		fail "unexpected managed state: $1"
	fi
}

cat > "$STATE" <<'EOF'
wattline.main.wan_access=0
firewall.unrelated=rule
firewall.unrelated.name=Keep me
firewall.wattline_http=rule
firewall.wattline_http.dest_port=9999
firewall.wattline_https=rule
firewall.wattline_https.dest_port=9998
EOF

[ -x "$HELPER" ] || fail "missing executable helper: $HELPER"
"$HELPER"
assert_absent firewall.wattline_http
assert_absent firewall.wattline_https
assert_line 'firewall.unrelated=rule'
assert_line 'firewall.unrelated.name=Keep me'
[ "$(wc -l < "$STATE.commands")" -eq 1 ] || fail "disabled reconciliation did not commit exactly once"
[ "$(wc -l < "$RELOADS")" -eq 1 ] || fail "disabled reconciliation did not reload exactly once"

cp "$STATE" "$TMP/disabled.before"
"$HELPER"
cmp -s "$STATE" "$TMP/disabled.before" || fail "disabled repeat changed UCI state"
[ "$(wc -l < "$STATE.commands")" -eq 1 ] || fail "disabled repeat committed"
[ "$(wc -l < "$RELOADS")" -eq 1 ] || fail "disabled repeat reloaded"

cat >> "$STATE" <<'EOF'
wattline.main.wan_access=1
wattline.main.http_enabled=1
wattline.main.port=8480
wattline.main.https_enabled=1
wattline.main.https_port=8481
firewall.wattline_http.family=ipv4
firewall.wattline_https.extra=hostile
EOF
"$HELPER"
for rule in wattline_http wattline_https; do
	assert_line "firewall.$rule=rule"
	assert_line "firewall.$rule.src=wan"
	assert_line "firewall.$rule.proto=tcp"
	assert_line "firewall.$rule.target=ACCEPT"
	assert_line "firewall.$rule.enabled=1"
done
assert_line 'firewall.wattline_http.name=Wattline HTTP'
assert_line 'firewall.wattline_http.dest_port=8480'
assert_line 'firewall.wattline_https.name=Wattline HTTPS'
assert_line 'firewall.wattline_https.dest_port=8481'
assert_absent firewall.wattline_http.family
assert_absent firewall.wattline_https.extra
grep -Fqx 'wattline: WAN access enabled: insecure — use TLS/VPN' "$LOG" || fail "missing exact WAN warning"
[ "$(wc -l < "$STATE.commands")" -eq 2 ] || fail "enabled reconciliation did not commit exactly once"
[ "$(wc -l < "$RELOADS")" -eq 2 ] || fail "enabled reconciliation did not reload exactly once"

cp "$STATE" "$TMP/enabled.before"
"$HELPER"
cmp -s "$STATE" "$TMP/enabled.before" || fail "enabled repeat changed UCI state"
[ "$(wc -l < "$STATE.commands")" -eq 2 ] || fail "enabled repeat committed"
[ "$(wc -l < "$RELOADS")" -eq 2 ] || fail "enabled repeat reloaded"
[ "$(grep -Fc 'wattline: WAN access enabled: insecure — use TLS/VPN' "$LOG")" -eq 1 ] || fail "no-op sync repeated WAN warning"

# Listener disablement removes only its corresponding named rule.
printf '%s\n' 'wattline.main.https_enabled=0' >> "$STATE"
"$HELPER"
assert_line 'firewall.wattline_http=rule'
assert_absent firewall.wattline_https
assert_line 'firewall.unrelated=rule'

# A committed disable remains apply-pending when reload fails, and the next
# no-op invocation retries the reload rather than silently accepting stale
# kernel firewall state.
printf '%s\n' 'wattline.main.wan_access=0' >> "$STATE"
FAIL_RELOAD="$TMP/fail-reload"
export FAIL_RELOAD
: > "$FAIL_RELOAD"
if "$HELPER"; then
	fail "firewall reload failure was ignored"
fi
[ -e "$APPLY_MARKER" ] || fail "failed reload did not leave apply marker"
assert_absent firewall.wattline_http
before_commits="$(wc -l < "$STATE.commands")"
rm -f "$FAIL_RELOAD"
"$HELPER"
[ ! -e "$APPLY_MARKER" ] || fail "successful retry did not clear apply marker"
[ "$(wc -l < "$STATE.commands")" -eq "$before_commits" ] || fail "apply retry committed unchanged UCI"

# Invalid manual UCI edits fail before changing any firewall section.
printf '%s\n' 'wattline.main.wan_access=1' >> "$STATE"
printf '%s\n' 'wattline.main.port=not-a-port' >> "$STATE"
cp "$STATE" "$TMP/invalid.before"
if "$HELPER"; then
	fail "invalid WAN port was accepted"
fi
cmp -s "$STATE" "$TMP/invalid.before" || fail "invalid WAN port partially changed UCI state"

echo "firewall-sync tests passed"
