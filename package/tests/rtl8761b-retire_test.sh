#!/bin/sh
# Exercises the installer's retirement of the removed wattline-rtl8761b
# package. The 0.1.5 installer activated that driver whenever it saw a
# supported adapter, so upgrading routers really do reach this code with the
# stock modules replaced.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
INSTALLER="$ROOT/package/install.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' 0 HUP INT TERM

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

# Run retire_rtl8761b in isolation. The rest of install.sh needs root, opkg,
# and a live router, so extract the function and drive it directly.
sed -n '/^warn()/,/^}/p;/^# 0.1.0 shipped/,/^}$/p' "$INSTALLER" >"$TMP/retire.sh"
grep -q 'retire_rtl8761b()' "$TMP/retire.sh" || fail 'could not extract retire_rtl8761b from install.sh'

# $1 = case name, $2 = "activated" to stage a complete stock backup,
# $3 = "nodriverctl" to omit driverctl, $4 = "restorefails" to make it fail.
setup_case() {
	case_dir="$TMP/$1"
	rm -rf "$case_dir"
	mkdir -p "$case_dir/root/usr/lib/wattline/rtl8761b" \
		"$case_dir/root/etc/init.d" \
		"$case_dir/root/etc/hotplug.d/usb" \
		"$case_dir/root/etc/wattline" \
		"$case_dir/bin"
	CALLS="$case_dir/calls"
	: >"$CALLS"

	if [ "${3:-}" != nodriverctl ]; then
		cat >"$case_dir/root/usr/lib/wattline/rtl8761b/driverctl" <<EOF
#!/bin/sh
printf 'driverctl %s\n' "\$1" >>"$CALLS"
[ "\$1" = restore ] && [ "${4:-}" = restorefails ] && exit 1
exit 0
EOF
		chmod +x "$case_dir/root/usr/lib/wattline/rtl8761b/driverctl"
	fi

	cat >"$case_dir/root/etc/init.d/wattline-rtl8761b" <<EOF
#!/bin/sh
printf 'init %s\n' "\$1" >>"$CALLS"
EOF
	chmod +x "$case_dir/root/etc/init.d/wattline-rtl8761b"
	touch "$case_dir/root/etc/hotplug.d/usb/20-wattline-rtl8761b" \
		"$case_dir/root/etc/wattline/rtl8761b.health" \
		"$case_dir/root/etc/wattline/rtl8761b.hotplug-enabled"

	if [ "${2:-}" = activated ]; then
		mkdir -p "$case_dir/root/etc/wattline/rtl8761b-stock"
		: >"$case_dir/root/etc/wattline/rtl8761b-stock/complete"
	fi

	cat >"$case_dir/bin/opkg" <<EOF
#!/bin/sh
case "\$1" in
	list-installed) echo 'wattline-rtl8761b - 0.1.5' ;;
	remove) printf 'opkg remove %s\n' "\$2" >>"$CALLS" ;;
esac
EOF
	chmod +x "$case_dir/bin/opkg"
}

run_case() {
	case_dir="$TMP/$1"
	(
		set -eu
		PATH="$case_dir/bin:$PATH"
		target_root="$case_dir/root"
		fail() {
			printf 'installer failed: %s\n' "$*" >&2
			exit 1
		}
		. "$TMP/retire.sh"
		retire_rtl8761b
	) >"$case_dir/out" 2>&1 || fail "$1: retire_rtl8761b exited non-zero: $(cat "$case_dir/out")"
}

assert_called() {
	grep -Fqx "$2" "$TMP/$1/calls" || fail "$1: expected call '$2', got: $(tr '\n' ';' <"$TMP/$1/calls")"
}

assert_not_called() {
	grep -Fqx "$2" "$TMP/$1/calls" && fail "$1: unexpected call '$2'"
	return 0
}

assert_absent() {
	[ -e "$TMP/$1/root/$2" ] && fail "$1: $2 should have been removed"
	return 0
}

assert_present() {
	[ -e "$TMP/$1/root/$2" ] || fail "$1: $2 should have been kept"
}

# An activated router restores, disables boot, removes the package, and clears
# every hook the package left behind.
setup_case activated activated
run_case activated
assert_called activated 'driverctl restore'
assert_called activated 'driverctl disable-boot'
assert_called activated 'init stop'
assert_called activated 'opkg remove wattline-rtl8761b'
assert_absent activated etc/init.d/wattline-rtl8761b
assert_absent activated etc/hotplug.d/usb/20-wattline-rtl8761b
assert_absent activated etc/wattline/rtl8761b.health
assert_absent activated etc/wattline/rtl8761b.hotplug-enabled

# A failed restore on an activated router keeps the package: driverctl is the
# only way back to the stock modules.
setup_case restore_failed activated '' restorefails
run_case restore_failed
assert_called restore_failed 'driverctl restore'
assert_called restore_failed 'driverctl disable-boot'
assert_not_called restore_failed 'opkg remove wattline-rtl8761b'
assert_present restore_failed usr/lib/wattline/rtl8761b/driverctl
grep -Fq 'still replaced' "$TMP/restore_failed/out" || fail 'restore_failed: missing warning'

# Installed but never activated: there is no stock backup, so restore fails
# with nothing to undo. The package must still be removed.
setup_case never_activated '' '' restorefails
run_case never_activated
assert_called never_activated 'driverctl restore'
assert_called never_activated 'opkg remove wattline-rtl8761b'
assert_absent never_activated etc/init.d/wattline-rtl8761b

# driverctl deleted by hand while the modules are still swapped: nothing can
# restore them, so keep the package and say so — but still disable boot the
# same way driverctl disable-boot would have (procd link + hotplug marker).
setup_case driverctl_gone activated nodriverctl
run_case driverctl_gone
assert_not_called driverctl_gone 'opkg remove wattline-rtl8761b'
assert_present driverctl_gone etc/init.d/wattline-rtl8761b
assert_called driverctl_gone 'init disable'
assert_called driverctl_gone 'init stop'
assert_absent driverctl_gone etc/wattline/rtl8761b.hotplug-enabled
grep -Fq 'driverctl is missing' "$TMP/driverctl_gone/out" || fail 'driverctl_gone: missing warning'

# driverctl absent and nothing was ever swapped: remove the leftovers.
setup_case driverctl_gone_stock '' nodriverctl
run_case driverctl_gone_stock
assert_called driverctl_gone_stock 'opkg remove wattline-rtl8761b'
assert_absent driverctl_gone_stock etc/init.d/wattline-rtl8761b

# A router that never had the package: a no-op that must not fail under set -eu.
clean="$TMP/clean"
mkdir -p "$clean/root/etc/wattline" "$clean/bin"
cat >"$clean/bin/opkg" <<'EOF'
#!/bin/sh
case "$1" in
	list-installed) echo 'wattlined - 0.1.6' ;;
	remove) echo "unexpected remove $2" ;;
esac
EOF
chmod +x "$clean/bin/opkg"
: >"$clean/calls"
run_case clean
[ -s "$TMP/clean/out" ] && fail "clean: expected no output, got: $(cat "$TMP/clean/out")"

echo 'RTL8761B retirement tests passed'
