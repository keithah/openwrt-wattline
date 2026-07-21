#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
HELPER="$ROOT/package/wattlined/usr/lib/wattline/restart-bluetooth-security"
TMP="${TMPDIR:-/tmp}/wattline-bluetooth-security-recovery.$$"
CALLS="$TMP/calls"
PROBES="$TMP/probes"
export CALLS PROBES

trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

assert_count() {
	expected=$1
	pattern=$2
	actual="$(grep -Fxc "$pattern" "$CALLS" || true)"
	[ "$actual" -eq "$expected" ] ||
		fail "expected $expected calls matching [$pattern], got $actual: $(tr '\n' ';' <"$CALLS")"
}

assert_no_forbidden_calls() {
	for forbidden in insmod rmmod reboot fw3 ip; do
		if grep -Eq "^${forbidden}( |$)" "$CALLS"; then
			fail "helper invoked forbidden command: $forbidden"
		fi
	done
}

cat >"$TMP/bin/service" <<'EOF'
#!/bin/sh
printf 'service %s\n' "$*" >>"$CALLS"
[ "${SERVICE_FAIL:-0}" -eq 0 ]
EOF

cat >"$TMP/bin/hciconfig" <<'EOF'
#!/bin/sh
printf 'hciconfig %s\n' "$*" >>"$CALLS"
count=0
[ ! -s "$PROBES" ] || count="$(cat "$PROBES")"
count=$((count + 1))
printf '%s\n' "$count" >"$PROBES"
[ "${HCICONFIG_SUCCESS_AT:-0}" -gt 0 ] && [ "$count" -ge "$HCICONFIG_SUCCESS_AT" ]
EOF

cat >"$TMP/bin/sleep" <<'EOF'
#!/bin/sh
printf 'sleep %s\n' "$*" >>"$CALLS"
EOF

for command in insmod rmmod reboot fw3 ip; do
	cat >"$TMP/bin/$command" <<EOF
#!/bin/sh
printf '$command %s\\n' "\$*" >>"\$CALLS"
exit 99
EOF
done
chmod +x "$TMP/bin/"*

[ -x "$HELPER" ] || fail 'packaged helper is missing or not executable'
grep -Fq 'VERSION := 0.1.5' "$ROOT/package/Makefile" || fail 'package version is not 0.1.5'
grep -Fq '$(OUT)/stage/usr/lib/wattline/restart-bluetooth-security' "$ROOT/package/Makefile" ||
	fail 'package does not mark the helper executable'

run_helper() {
	: >"$CALLS"
	: >"$PROBES"
	BLUETOOTH_SERVICE="$TMP/bin/service" \
		HCICONFIG="$TMP/bin/hciconfig" \
		SLEEP="$TMP/bin/sleep" \
		READY_ATTEMPTS="$1" \
		PATH="$TMP/bin:/usr/bin:/bin" \
		sh "$HELPER"
}

HCICONFIG_SUCCESS_AT=3; export HCICONFIG_SUCCESS_AT
run_helper 5 || fail 'third readiness probe did not succeed'
assert_count 1 'service restart'
assert_count 3 'hciconfig hci0'
assert_count 2 'sleep 1'
assert_no_forbidden_calls

HCICONFIG_SUCCESS_AT=0; export HCICONFIG_SUCCESS_AT
if run_helper 4 >/dev/null 2>&1; then
	fail 'helper succeeded although hci0 never became ready'
fi
assert_count 1 'service restart'
assert_count 4 'hciconfig hci0'
assert_no_forbidden_calls

SERVICE_FAIL=1; export SERVICE_FAIL
if run_helper 6 >/dev/null 2>&1; then
	fail 'helper ignored bluetoothd restart failure'
fi
assert_count 1 'service restart'
assert_count 0 'hciconfig hci0'
assert_no_forbidden_calls
unset SERVICE_FAIL

echo 'Bluetooth security recovery tests passed'
