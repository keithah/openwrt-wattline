#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
DRIVERCTL="$ROOT/package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl"
PACKAGE_ROOT="$ROOT/package/wattline-rtl8761b"
PAYLOAD="$PACKAGE_ROOT/usr/lib/wattline/rtl8761b"
TMP="${TMPDIR:-/tmp}/wattline-rtl8761b-test.$$"
REAL_PATH=/usr/bin:/bin:/usr/sbin:/sbin
REAL_MODINFO="$(command -v modinfo)"

trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

assert_file() {
	[ -f "$1" ] || fail "missing file $1"
}

assert_same() {
	cmp "$1" "$2" >/dev/null || fail "$1 differs from $2"
}

assert_eq() {
	[ "$1" = "$2" ] || fail "expected [$2], got [$1]"
}

make_fakes() {
	mkdir -p "$TMP/bin"
	for command in cp mv rmmod insmod hciconfig modinfo; do
		ln -s fake-command "$TMP/bin/$command"
	done
	cat >"$TMP/bin/fake-command" <<'EOF'
#!/bin/sh
set -eu
command="${0##*/}"
if [ "$command" = modinfo ]; then
	if [ "${MODINFO_NO_FIELD:-}" = 1 ] && [ "${1:-}" = -F ]; then
		exit 255
	fi
	exec "$REAL_MODINFO" "$@"
fi
count_file="$COUNTERS/$command"
count=0
[ ! -f "$count_file" ] || count="$(cat "$count_file")"
count=$((count + 1))
printf '%s\n' "$count" >"$count_file"
printf '%s %s\n' "$command" "$*" >>"$CALLS"
[ "${FAIL_AT:-}" != "$command:$count" ] || exit 71
case "$command" in
	cp|mv) exec "/bin/$command" "$@" ;;
	rmmod|insmod|hciconfig) exit 0 ;;
esac
EOF
	chmod +x "$TMP/bin/fake-command"
}

setup_case() {
	CASE="$TMP/case"
	rm -rf "$CASE"
	mkdir -p "$CASE/root/lib/modules/5.4.211" \
		"$CASE/root/etc/modules.d" "$CASE/root/var/lock" \
		"$CASE/root/sys/bus/usb/devices" "$CASE/root/sys/class/bluetooth/hci0" \
		"$CASE/state" "$CASE/counters"
	printf 'stock-intel\n' >"$CASE/root/lib/modules/5.4.211/btintel.ko"
	printf 'stock-usb\n' >"$CASE/root/lib/modules/5.4.211/btusb.ko"
	printf '%s\n' bluetooth bnep btusb hci_uart hidp rfcomm >"$CASE/root/etc/modules.d/bluetooth"
	printf '%s\n' \
		'btusb 1 0 - Live 0' \
		'btrtl 1 1 btusb, Live 0' \
		'btintel 1 1 btusb, Live 0' >"$CASE/proc-modules"
	: >"$CASE/calls"
cat >"$CASE/wattlined" <<'EOF'
#!/bin/sh
printf 'wattlined %s\n' "$*" >>"$CALLS"
[ "$1" = health ] && [ "${WATTLINE_HEALTH_FAIL:-}" = 1 ] && exit 71
exit 0
EOF
	chmod +x "$CASE/wattlined"
	export ROOT_PREFIX="$CASE/root"
	export PACKAGE_ROOT PAYLOAD_DIR="$PAYLOAD" STATE_DIR="$CASE/root/etc/wattline/rtl8761b-stock"
	export LOCK_FILE="$CASE/root/var/lock/wattline-rtl8761b.lock"
	export KERNEL_RELEASE=5.4.211 MACHINE=aarch64 PROC_MODULES="$CASE/proc-modules"
	export SYS_USB="$CASE/root/sys/bus/usb/devices" WATTLINE_SERVICE="$CASE/wattlined"
	export CALLS="$CASE/calls" COUNTERS="$CASE/counters" REAL_MODINFO PATH="$TMP/bin:$REAL_PATH"
	unset FAIL_AT
	unset WATTLINE_HEALTH_FAIL
	unset MODINFO_NO_FIELD
}

add_usb() {
	dir="$SYS_USB/$1"
	mkdir -p "$dir"
	printf '%s\n' "$2" >"$dir/idVendor"
	printf '%s\n' "$3" >"$dir/idProduct"
}

run_admit_detect() {
	setup_case
	"$DRIVERCTL" admit
	[ ! -e "$STATE_DIR" ] || fail 'admit mutated state'
	[ ! -s "$CALLS" ] || fail 'admit invoked mutating commands'
	MODINFO_NO_FIELD=1 "$DRIVERCTL" admit

	if KERNEL_RELEASE=6.6.0 "$DRIVERCTL" admit >/dev/null 2>&1; then
		fail 'unsupported kernel admitted'
	fi
	[ ! -e "$STATE_DIR" ] || fail 'bad admit mutated state'

	add_usb one 2357 0604
	assert_eq "$("$DRIVERCTL" detect)" '2357:0604'
	rm -rf "$SYS_USB/one"
	add_usb two 0BDA 8771
	assert_eq "$("$DRIVERCTL" detect)" '0bda:8771'
	rm -rf "$SYS_USB/two"
	add_usb three 0a12 0001
	if "$DRIVERCTL" detect >"$CASE/detect-output"; then
		fail 'unsupported USB adapter detected'
	fi
	[ ! -s "$CASE/detect-output" ] || fail 'unsupported detection printed output'
}

expected_runtime_calls() {
	cat <<EOF
wattlined stop
hciconfig hci0 down
rmmod btusb
rmmod btrtl
rmmod btintel
insmod $PAYLOAD/modules/5.4.211/btintel.ko
insmod $PAYLOAD/modules/5.4.211/btrtl.ko
insmod $PAYLOAD/modules/5.4.211/btusb.ko
hciconfig hci0 up
wattlined restart
wattlined health
EOF
}

runtime_calls() {
	grep -E '^(wattlined|hciconfig|rmmod|insmod) ' "$CALLS"
}

run_activate_repair() {
	setup_case
	add_usb one 2357 0604
	stock_intel="$(sha256sum "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko" | awk '{print $1}')"
	stock_usb="$(sha256sum "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko" | awk '{print $1}')"
	"$DRIVERCTL" activate --require-device
	assert_same "$PAYLOAD/modules/5.4.211/btintel.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko"
	assert_same "$PAYLOAD/modules/5.4.211/btrtl.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btrtl.ko"
	assert_same "$PAYLOAD/modules/5.4.211/btusb.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko"
	! grep -Fxq btusb "$ROOT_PREFIX/etc/modules.d/bluetooth" || fail 'btusb remains in module list'
	grep -Fxq bluetooth "$ROOT_PREFIX/etc/modules.d/bluetooth" || fail 'unrelated module removed'
	assert_eq "$(runtime_calls)" "$(expected_runtime_calls)"
	assert_eq "$("$DRIVERCTL" status)" packaged
	backup_intel="$(sha256sum "$STATE_DIR/btintel.ko" | awk '{print $1}')"
	backup_usb="$(sha256sum "$STATE_DIR/btusb.ko" | awk '{print $1}')"
	assert_eq "$backup_intel" "$stock_intel"
	assert_eq "$backup_usb" "$stock_usb"
	assert_file "$STATE_DIR/btrtl.ko.absent"

	printf 'vendor-overwrite\n' >"$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko"
	rm -rf "$CASE/counters" && mkdir "$CASE/counters"
	: >"$CALLS"
	"$DRIVERCTL" activate --require-device
	assert_same "$PAYLOAD/modules/5.4.211/btusb.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko"
	assert_eq "$(sha256sum "$STATE_DIR/btintel.ko" | awk '{print $1}')" "$backup_intel"
	assert_eq "$(sha256sum "$STATE_DIR/btusb.ko" | awk '{print $1}')" "$backup_usb"
}

prepare_stock_with_backup() {
	setup_case
	add_usb one 2357 0604
	"$DRIVERCTL" activate --require-device
	/bin/cp "$STATE_DIR/btintel.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko"
	/bin/cp "$STATE_DIR/btusb.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko"
	rm -f "$ROOT_PREFIX/lib/modules/5.4.211/btrtl.ko"
	/bin/cp "$STATE_DIR/bluetooth" "$ROOT_PREFIX/etc/modules.d/bluetooth"
	rm -rf "$CASE/counters" && mkdir "$CASE/counters"
	: >"$CALLS"
}

run_failures() {
	for point in \
		cp:1 cp:2 cp:3 cp:4 \
		mv:1 mv:2 mv:3 mv:4 \
		rmmod:1 rmmod:2 rmmod:3 \
		insmod:1 insmod:2 insmod:3; do
		prepare_stock_with_backup
		export FAIL_AT="$point"
		if "$DRIVERCTL" activate --require-device >/dev/null 2>&1; then
			printf '%s\n' 'calls:' >&2
			cat "$CALLS" >&2
			printf 'counters: ' >&2
			for counter in "$COUNTERS"/*; do [ ! -f "$counter" ] || printf '%s=%s ' "${counter##*/}" "$(cat "$counter")" >&2; done
			printf '\n' >&2
			fail "activation unexpectedly succeeded at $point"
		fi
		unset FAIL_AT
		assert_same "$STATE_DIR/btintel.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko"
		assert_same "$STATE_DIR/btusb.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko"
		[ ! -e "$ROOT_PREFIX/lib/modules/5.4.211/btrtl.ko" ] || fail "btrtl survived rollback at $point"
		assert_same "$STATE_DIR/bluetooth" "$ROOT_PREFIX/etc/modules.d/bluetooth"
		if find "$ROOT_PREFIX" \( -name '*.wattline-new.*' -o -name '*.wattline-old.*' \) | grep -q .; then
			fail "temporary file remains at $point"
		fi
	done
	prepare_stock_with_backup
	export WATTLINE_HEALTH_FAIL=1
	if "$DRIVERCTL" activate --require-device >/dev/null 2>&1; then fail 'activation unexpectedly succeeded on health failure'; fi
	assert_same "$STATE_DIR/btintel.ko" "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko"
	[ ! -e "$ROOT_PREFIX/etc/wattline/rtl8761b.rollback" ] || fail 'rollback marker survived recovery'
}

run_restore() {
	setup_case
	add_usb one 2357 0604
	"$DRIVERCTL" activate --require-device
	"$DRIVERCTL" restore
	printf 'stock-intel\n' | cmp - "$ROOT_PREFIX/lib/modules/5.4.211/btintel.ko" || fail 'btintel not restored'
	printf 'stock-usb\n' | cmp - "$ROOT_PREFIX/lib/modules/5.4.211/btusb.ko" || fail 'btusb not restored'
	[ ! -e "$ROOT_PREFIX/lib/modules/5.4.211/btrtl.ko" ] || fail 'packaged-only btrtl not removed'
	grep -Fxq btusb "$ROOT_PREFIX/etc/modules.d/bluetooth" || fail 'module list not restored'
	[ ! -e "$STATE_DIR" ] || fail 'backup not removed after restore'

	setup_case
	add_usb one 2357 0604
	"$DRIVERCTL" activate --require-device
	rm -rf "$CASE/counters" && mkdir "$CASE/counters"
	export FAIL_AT=insmod:1
	if "$DRIVERCTL" restore >/dev/null 2>&1; then
		fail 'restore unexpectedly succeeded with load failure'
	fi
	unset FAIL_AT
	assert_file "$STATE_DIR/complete"
}

[ -x "$DRIVERCTL" ] || fail "driverctl is not executable: $DRIVERCTL"
make_fakes

selection="${*:-admit detect activate failures restore}"
case " $selection " in *' admit '*|*' detect '*) run_admit_detect ;; esac
case " $selection " in *' activate '*) run_activate_repair ;; esac
case " $selection " in *' failures '*) run_failures ;; esac
case " $selection " in *' restore '*) run_restore ;; esac

echo 'RTL8761B driver control tests passed'
