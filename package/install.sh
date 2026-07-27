#!/bin/sh
# Install the Wattline packages from the project-maintained opkg feed.
set -eu

feed_url="${WATTLINE_FEED_URL:-https://keithah.github.io/openwrt-packages}"
package_dir="${WATTLINE_PACKAGE_DIR:-}"
target_root="${WATTLINE_ROOT:-/}"
feeds_file="$target_root/etc/opkg/customfeeds.conf"
keys_dir="$target_root/etc/opkg/keys"
feed_key_file="$keys_dir/f6c72c675c844b91"
feed_key='untrusted comment: Keith OpenWrt package feed
RWT2xyxnXIRLkZzbs1HvD+48GPkSqoNPCZVCOw49GUdTg2O7Cv9LzMtx'

fail() {
	printf '%s\n' "wattline installer: $*" >&2
	exit 1
}

warn() {
	printf '%s\n' "wattline installer: $*" >&2
}

# 0.1.0 shipped an optional wattline-rtl8761b package that overwrote the stock
# btusb/btrtl/btintel modules and /etc/modules.d/bluetooth, keeping the
# originals only in /etc/wattline/rtl8761b-stock. That package is gone from the
# feed and its prerm was inert, so its own driverctl is the only thing that can
# undo the swap. Retire it here, while it is still on the router.
retire_rtl8761b() {
	prefix="${target_root%/}"
	driverctl="$prefix/usr/lib/wattline/rtl8761b/driverctl"
	boot_init="$prefix/etc/init.d/wattline-rtl8761b"
	# driverctl swapped the stock modules only after committing a complete
	# backup, so this marker — not the exit status of restore — is what says
	# whether the router is still running the out-of-tree drivers. A package
	# that was installed but never activated has no backup and nothing to undo,
	# and restore fails on it ("no complete stock backup"); that must not be
	# mistaken for a router stranded on the packaged modules.
	stock_backup="$prefix/etc/wattline/rtl8761b-stock/complete"

	restored=yes
	if [ -x "$driverctl" ]; then
		ROOT_PREFIX="$prefix" "$driverctl" restore || restored=no
	elif [ -f "$stock_backup" ]; then
		restored=no
	fi

	# Tear down boot and hotplug activation however the restore went, because
	# the warning below promises they are off. Do not rely on driverctl or the
	# init hook being present: the init hook's start() is a no-op without the
	# health marker and the USB hook exits early without its own marker, so
	# clearing both markers stops the force-load even when the S15 link or the
	# hook itself survives.
	if [ -x "$boot_init" ]; then
		if [ -x "$driverctl" ]; then
			ROOT_PREFIX="$prefix" "$driverctl" disable-boot || true
		else
			"$boot_init" disable >/dev/null 2>&1 || true
		fi
		"$boot_init" stop >/dev/null 2>&1 || true
	fi
	rm -f "$prefix/etc/wattline/rtl8761b.health" \
		"$prefix/etc/wattline/rtl8761b.hotplug-enabled"

	if [ "$restored" = no ] && [ -f "$stock_backup" ]; then
		# The packaged modules are still the ones on disk. Keep whatever is
		# left of the package rather than deleting the only way back.
		warn 'the stock Bluetooth modules are still replaced by wattline-rtl8761b; boot activation has been disabled but the package was left installed'
		if [ -x "$driverctl" ]; then
			warn 'recover manually: /usr/lib/wattline/rtl8761b/driverctl restore && opkg remove wattline-rtl8761b'
		else
			warn "driverctl is missing; restore the originals from ${stock_backup%/complete} by hand, then: opkg remove wattline-rtl8761b"
		fi
		return 0
	fi

	if opkg list-installed 2>/dev/null | grep -q '^wattline-rtl8761b '; then
		opkg remove wattline-rtl8761b || fail 'could not remove wattline-rtl8761b'
	fi

	rm -f "$prefix/etc/init.d/wattline-rtl8761b" \
		"$prefix/etc/hotplug.d/usb/20-wattline-rtl8761b" \
		"$prefix/etc/wattline/rtl8761b.health" \
		"$prefix/etc/wattline/rtl8761b.hotplug-enabled" \
		"$prefix/etc/wattline/rtl8761b.rollback"
}

[ "$(id -u)" = 0 ] || fail 'must be run as root'
command -v opkg >/dev/null 2>&1 || fail 'opkg is required'
command -v wget >/dev/null 2>&1 || fail 'wget is required'

if ! architectures=$(opkg print-architecture); then
	fail 'could not determine package architectures'
fi
if ! printf '%s\n' "$architectures" | awk '$2 == "aarch64_cortex-a53" { found = 1 } END { exit !found }'; then
	fail 'this installer requires aarch64_cortex-a53'
fi

[ -d "$target_root/etc/opkg" ] || fail "missing $target_root/etc/opkg"
[ -d "$keys_dir" ] || fail "missing $keys_dir"
[ -f "$feeds_file" ] || : >"$feeds_file"

if [ -e "$target_root/etc/config/glconfig" ] || [ -e "$target_root/usr/lib/oui-httpd" ]; then
	ui_package='gl-app-wattline'
	dashboard_url='GL admin: Applications -> Wattline'
else
	ui_package='luci-app-wattline'
	dashboard_url='http://router-address/cgi-bin/luci/admin/services/wattline'
fi

feeds_dir=$(dirname "$feeds_file")
tmp_file=$(mktemp "$feeds_dir/.customfeeds.conf.XXXXXX")
key_tmp=$(mktemp "$keys_dir/.keithah-key.XXXXXX")
trap 'rm -f "$tmp_file" "$key_tmp"' 0 HUP INT TERM

printf '%s\n' "$feed_key" >"$key_tmp"
chmod 0644 "$key_tmp"
mv "$key_tmp" "$feed_key_file"

# This feed is managed exclusively by this installer. Keep every other feed
# line byte-for-byte while replacing all previous managed entries with one.
awk '$1 == "src/gz" && ($2 == "wattline" || $2 == "starwatch" || $2 == "keithah") { next } { print }' "$feeds_file" >"$tmp_file"
printf 'src/gz keithah %s\n' "$feed_url" >>"$tmp_file"

# mktemp normally creates mode 0600. Retain the existing file's access mode
# and owner when the platform's stat format can provide them.
if metadata=$(stat -c '%a %u %g' "$feeds_file" 2>/dev/null); then
	set -- $metadata
	chmod "$1" "$tmp_file"
	chown "$2:$3" "$tmp_file" 2>/dev/null || fail 'could not preserve feed file ownership'
elif metadata=$(stat -f '%Lp %u %g' "$feeds_file" 2>/dev/null); then
	set -- $metadata
	chmod "$1" "$tmp_file"
	chown "$2:$3" "$tmp_file" 2>/dev/null || fail 'could not preserve feed file ownership'
fi
mv "$tmp_file" "$feeds_file"
trap - 0 HUP INT TERM

retire_rtl8761b

if [ -n "$package_dir" ]; then
	# Development/release validation mode. Globs are resolved on the router and
	# must identify exactly one build of each required package.
	opkg install "$package_dir"/wattline-bt_*.ipk \
		"$package_dir"/wattlined_*.ipk "$package_dir"/"${ui_package}"_*.ipk
else
	opkg update
	opkg install wattline-bt wattlined "$ui_package"
fi

/etc/init.d/bluetoothd enable
/etc/init.d/bluetoothd start
/etc/init.d/wattlined enable
/etc/init.d/wattlined start
/etc/init.d/wattlined health || fail 'wattlined failed its startup health check'

printf 'Installed Wattline with %s. Dashboard: %s\n' "$ui_package" "$dashboard_url"
