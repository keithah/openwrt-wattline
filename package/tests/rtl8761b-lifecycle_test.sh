#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
BASE="$ROOT/package/wattline-rtl8761b"
TMP="${TMPDIR:-/tmp}/wattline-rtl8761b-lifecycle.$$"
CALLS="$TMP/calls"
export CALLS

trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"
: >"$CALLS"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

assert_calls() {
	expected=$1
	actual="$(cat "$CALLS")"
	[ "$actual" = "$expected" ] || fail "expected calls [$expected], got [$actual]"
}

cat >"$TMP/driverctl" <<'EOF'
#!/bin/sh
printf 'driverctl %s\n' "$*" >>"$CALLS"
EOF
cat >"$TMP/init-service" <<'EOF'
#!/bin/sh
printf 'init %s\n' "$*" >>"$CALLS"
EOF
cat >"$TMP/bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
	-r) printf '%s\n' "${FAKE_KERNEL:-5.4.211}" ;;
	-m) printf '%s\n' "${FAKE_MACHINE:-aarch64}" ;;
	*) exit 2 ;;
esac
EOF
chmod +x "$TMP/driverctl" "$TMP/init-service" "$TMP/bin/uname"
export DRIVERCTL="$TMP/driverctl" INIT_SERVICE="$TMP/init-service" PATH="$TMP/bin:/usr/bin:/bin"

for file in CONTROL/control CONTROL/preinst CONTROL/postinst CONTROL/prerm \
	etc/init.d/wattline-rtl8761b etc/hotplug.d/usb/20-wattline-rtl8761b; do
	[ -s "$BASE/$file" ] || fail "missing lifecycle file $file"
done

IPKG_INSTROOT= sh "$BASE/CONTROL/preinst"
: >"$CALLS"
if FAKE_KERNEL=6.6.0 IPKG_INSTROOT= sh "$BASE/CONTROL/preinst" >/dev/null 2>&1; then
	fail 'preinst admitted kernel 6.6.0'
fi
assert_calls ''
if FAKE_MACHINE=x86_64 IPKG_INSTROOT= sh "$BASE/CONTROL/preinst" >/dev/null 2>&1; then
	fail 'preinst admitted x86_64'
fi
assert_calls ''

IPKG_INSTROOT=/staging sh "$BASE/CONTROL/postinst"
assert_calls ''
IPKG_INSTROOT= sh "$BASE/CONTROL/postinst"
assert_calls ''

: >"$CALLS"
IPKG_INSTROOT=/staging sh "$BASE/CONTROL/prerm"
assert_calls ''
IPKG_INSTROOT= sh "$BASE/CONTROL/prerm"
assert_calls ''

: >"$CALLS"
. "$BASE/etc/init.d/wattline-rtl8761b"
HEALTH_MARKER="$TMP/health"
start
assert_calls ''
: >"$HEALTH_MARKER"
start
assert_calls 'driverctl activate'

# wattlined postinst must retain defaults setup but never perform runtime
# service activation/restart or driver operations.
for forbidden in driverctl insmod rmmod hciconfig 'init.d/wattlined enable' 'init.d/wattlined restart'; do
	if grep -Fq "$forbidden" "$ROOT/package/wattlined/CONTROL/postinst"; then
		fail "wattlined postinst contains forbidden runtime action: $forbidden"
	fi
done

hotplug="$BASE/etc/hotplug.d/usb/20-wattline-rtl8761b"
for product in 2357/0604/0100 0BDA/8771/0200; do
	: >"$CALLS"
	ACTION=add PRODUCT="$product" sh "$hotplug"
	assert_calls 'driverctl activate --require-device'
done
for row in 'remove 2357/0604/0100' 'add 0a12/0001/0100' 'add malformed'; do
	: >"$CALLS"
	set -- $row
	ACTION=$1 PRODUCT=$2 sh "$hotplug"
	assert_calls ''
done

grep -Fxq 'Package: wattline-rtl8761b' "$BASE/CONTROL/control"
grep -Fxq 'Version: 0.1.2' "$BASE/CONTROL/control"
grep -Fxq 'Architecture: aarch64_cortex-a53' "$BASE/CONTROL/control"
grep -Fxq 'Depends: wattline-bt' "$BASE/CONTROL/control"

echo 'RTL8761B lifecycle tests passed'

# Boot hook controls require health and refuse rollback; they only call init enable/disable.
for required in 'enable-boot' 'disable-boot' 'HEALTH_MARKER' 'ROLLBACK_MARKER'; do
	grep -Fq "$required" "$BASE/usr/lib/wattline/rtl8761b/driverctl" || fail "driverctl missing boot control token: $required"
done
BOOT_INIT="$TMP/boot-init"
cat >"$BOOT_INIT" <<'EOF2'
#!/bin/sh
printf 'boot %s\n' "$*" >>"$CALLS"
EOF2
chmod +x "$BOOT_INIT"
: >"$TMP/health"
: >"$CALLS"
HEALTH_MARKER="$TMP/health" ROLLBACK_MARKER="$TMP/rollback" BOOT_INIT="$BOOT_INIT" \
	sh "$BASE/usr/lib/wattline/rtl8761b/driverctl" enable-boot
assert_calls 'boot enable'
: >"$CALLS"
HEALTH_MARKER="$TMP/missing" ROLLBACK_MARKER="$TMP/rollback" BOOT_INIT="$BOOT_INIT" \
	sh "$BASE/usr/lib/wattline/rtl8761b/driverctl" enable-boot >/dev/null 2>&1 && fail 'enable-boot ignored missing health marker'
assert_calls ''
: >"$TMP/rollback"
HEALTH_MARKER="$TMP/health" ROLLBACK_MARKER="$TMP/rollback" BOOT_INIT="$BOOT_INIT" \
	sh "$BASE/usr/lib/wattline/rtl8761b/driverctl" enable-boot >/dev/null 2>&1 && fail 'enable-boot ignored rollback marker'
: >"$CALLS"
HEALTH_MARKER="$TMP/health" ROLLBACK_MARKER="$TMP/rollback" BOOT_INIT="$BOOT_INIT" \
	sh "$BASE/usr/lib/wattline/rtl8761b/driverctl" disable-boot
assert_calls 'boot disable'
