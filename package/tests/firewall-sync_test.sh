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
UCI_PENDING="$TMP/uci.pending"
COMMITTED="$TMP/uci.committed"
export STATE LOG RELOADS APPLY_MARKER UCI_PENDING COMMITTED

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
		: > "$UCI_PENDING"
		if [ -e "${INTERRUPT_AFTER_MUTATION:-}" ]; then
			rm -f "$INTERRUPT_AFTER_MUTATION"
			exit 99
		fi
		;;
	delete)
		key="$1"
		tmp="${STATE}.tmp"
		awk -v key="$key" 'index($0, key "=") != 1 && index($0, key ".") != 1' "$STATE" > "$tmp"
		mv "$tmp" "$STATE"
		: > "$UCI_PENDING"
		if [ -e "${INTERRUPT_AFTER_MUTATION:-}" ]; then
			rm -f "$INTERRUPT_AFTER_MUTATION"
			exit 99
		fi
		;;
	commit)
		[ ! -e "${FAIL_COMMIT:-}" ] || exit 1
		cp "$STATE" "$COMMITTED"
		rm -f "$UCI_PENDING"
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
set -eu
[ "$1" = reload ]
printf 'reload\n' >> "$RELOADS"
[ ! -e "${FAIL_RELOAD:-}" ]
[ ! -e "$UCI_PENDING" ]
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
cp "$STATE" "$COMMITTED"

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
grep -Fqx reload "$APPLY_MARKER" || fail "failed reload did not preserve reload phase"
assert_absent firewall.wattline_http
before_commits="$(wc -l < "$STATE.commands")"
rm -f "$FAIL_RELOAD"
"$HELPER"
[ ! -e "$APPLY_MARKER" ] || fail "successful retry did not clear apply marker"
[ "$(wc -l < "$STATE.commands")" -eq "$before_commits" ] || fail "reload-phase retry repeated UCI commit"

# A managed mutation discovered while a reload is pending must first move the
# durable marker back to commit. Simulate interruption after that mutation and
# prove the next invocation commits before it reloads.
printf '%s\n' 'wattline.main.wan_access=1' >> "$STATE"
FAIL_RELOAD="$TMP/fail-reload"
: > "$FAIL_RELOAD"
if "$HELPER"; then
	fail "second firewall reload failure was ignored"
fi
grep -Fqx reload "$APPLY_MARKER" || fail "second failed reload lost reload phase"
before_warnings="$(grep -Fc 'wattline: WAN access enabled: insecure — use TLS/VPN' "$LOG")"
printf '%s\n' 'wattline.main.port=8490' >> "$STATE"
INTERRUPT_AFTER_MUTATION="$TMP/interrupt-reload-phase"
export INTERRUPT_AFTER_MUTATION
: > "$INTERRUPT_AFTER_MUTATION"
if "$HELPER"; then
	fail "reload-phase mutation interruption unexpectedly succeeded"
fi
grep -Fqx commit "$APPLY_MARKER" || fail "new mutation did not transition reload phase to commit"
[ -e "$UCI_PENDING" ] || fail "fake UCI did not retain interrupted pending delta"
rm -f "$FAIL_RELOAD"
before_commits="$(wc -l < "$STATE.commands")"
"$HELPER"
[ "$(wc -l < "$STATE.commands")" -eq "$((before_commits + 1))" ] || fail "reload-phase mutation was not committed"
[ ! -e "$UCI_PENDING" ] || fail "successful commit left fake UCI pending"
[ ! -e "$APPLY_MARKER" ] || fail "reload-phase mutation retry did not clear marker"
[ "$(grep -Fc 'wattline: WAN access enabled: insecure — use TLS/VPN' "$LOG")" -eq "$((before_warnings + 1))" ] || fail "reload-phase mutation did not emit WAN warning"

# The durable marker precedes the first mutation. Simulate interruption after
# deleting only the first owned rule; the next run completes the disable,
# commits the staged delta, reloads, and only then clears the marker.
printf '%s\n' 'wattline.main.wan_access=1' 'wattline.main.port=8480' 'wattline.main.https_enabled=1' >> "$STATE"
"$HELPER"
printf '%s\n' 'wattline.main.wan_access=0' >> "$STATE"
INTERRUPT_AFTER_MUTATION="$TMP/interrupt-after-mutation"
export INTERRUPT_AFTER_MUTATION
: > "$INTERRUPT_AFTER_MUTATION"
if "$HELPER"; then
	fail "interrupted firewall mutation unexpectedly succeeded"
fi
[ -e "$APPLY_MARKER" ] || fail "mutation began before durable apply marker"
grep -Fqx commit "$APPLY_MARKER" || fail "interruption did not preserve commit phase"
if ! grep -Eq '^firewall\.wattline_(http|https)=' "$STATE"; then
	fail "interruption hook did not stop after the first deletion"
fi
before_commits="$(wc -l < "$STATE.commands")"
"$HELPER"
assert_absent firewall.wattline_http
assert_absent firewall.wattline_https
[ "$(wc -l < "$STATE.commands")" -eq "$((before_commits + 1))" ] || fail "interrupted retry did not commit"
[ ! -e "$APPLY_MARKER" ] || fail "interrupted retry cleared marker before successful reload"

# Commit failure also preserves the marker and staged disable. A retry must
# commit again before reload rather than treating canonical staged UCI as done.
printf '%s\n' 'wattline.main.wan_access=1' >> "$STATE"
"$HELPER"
printf '%s\n' 'wattline.main.wan_access=0' >> "$STATE"
FAIL_COMMIT="$TMP/fail-commit"
export FAIL_COMMIT
: > "$FAIL_COMMIT"
before_reloads="$(wc -l < "$RELOADS")"
if "$HELPER"; then
	fail "firewall commit failure was ignored"
fi
[ -e "$APPLY_MARKER" ] || fail "commit failure cleared apply marker"
grep -Fqx commit "$APPLY_MARKER" || fail "commit failure lost commit phase"
[ "$(wc -l < "$RELOADS")" -eq "$before_reloads" ] || fail "reload ran after failed commit"
[ -e "$UCI_PENDING" ] || fail "commit failure lost fake pending UCI delta"
rm -f "$FAIL_COMMIT"
before_commits="$(wc -l < "$STATE.commands")"
"$HELPER"
[ "$(wc -l < "$STATE.commands")" -eq "$((before_commits + 1))" ] || fail "commit-failure retry did not commit"
[ ! -e "$APPLY_MARKER" ] || fail "commit-failure retry did not clear marker"

# If the marker itself cannot be created, UCI must remain untouched.
printf '%s\n' 'wattline.main.wan_access=1' >> "$STATE"
mkdir "$TMP/not-a-marker"
APPLY_MARKER="$TMP/not-a-marker"
export APPLY_MARKER
cp "$STATE" "$TMP/marker-failure.before"
if "$HELPER"; then
	fail "unwritable apply marker was accepted"
fi
cmp -s "$STATE" "$TMP/marker-failure.before" || fail "UCI mutated before apply marker creation"
APPLY_MARKER="$TMP/firewall.pending"
export APPLY_MARKER

# Invalid manual UCI edits fail before changing any firewall section.
printf '%s\n' 'wattline.main.wan_access=1' >> "$STATE"
printf '%s\n' 'wattline.main.port=not-a-port' >> "$STATE"
cp "$STATE" "$TMP/invalid.before"
if "$HELPER"; then
	fail "invalid WAN port was accepted"
fi
cmp -s "$STATE" "$TMP/invalid.before" || fail "invalid WAN port partially changed UCI state"

grep -Fq "trap 'lock -u \"\$LOCK_FILE\"' EXIT" "$HELPER" || fail "unlock is not EXIT-only"
grep -Fq "trap 'exit 1' HUP INT TERM" "$HELPER" || fail "signals do not exit through EXIT unlock"

echo "firewall-sync tests passed"
