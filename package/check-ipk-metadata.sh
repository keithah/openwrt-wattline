#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
	echo "usage: $0 package.ipk..." >&2
	exit 2
fi

# GNU tar for consistent -tv listing format (owner shown as "0/0"). The
# Makefile passes its own TAR so build and check always use the same binary.
TAR="${TAR:-$(command -v gtar || command -v gnutar || echo tar)}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

failed=0
for ipk in "$@"; do
	rm -rf "$tmp"/*
	"$TAR" -xzf "$ipk" -C "$tmp" ./control.tar.gz ./data.tar.gz
	control_version="$("$TAR" -xOzf "$tmp/control.tar.gz" ./control | sed -n 's/^Version:[[:space:]]*//p')"
	case "$(basename "$ipk")" in
		*_"$control_version"_*.ipk) ;;
		*)
			echo "$ipk: control Version '$control_version' does not match filename" >&2
			failed=1
			;;
	esac
	for archive in control.tar.gz data.tar.gz; do
		listing="$tmp/$archive.list"
		"$TAR" --numeric-owner -tvzf "$tmp/$archive" > "$listing"

		if awk '$2 != "0/0" { print; bad = 1 } END { exit !bad }' "$listing"; then
			echo "$ipk: $archive contains non-root ownership" >&2
			failed=1
		fi

		if awk 'substr($1, 1, 1) == "d" && $6 != "./etc/wattline/" && $6 != "./etc/wattline/tls/" && $1 != "drwxr-xr-x" { print; bad = 1 } END { exit !bad }' "$listing"; then
			echo "$ipk: $archive contains unexpected non-0755 directories" >&2
			failed=1
		fi
	done

	if "$TAR" -tzf "$tmp/data.tar.gz" | grep -E '(^|/)(server\.key|tokens\.json|rtl8761b-stock)(/|$)' >/dev/null; then
		echo "$ipk: private key, token store, or driver backup must not be shipped" >&2
		failed=1
	fi

	case "$(basename "$ipk")" in
		wattlined_*.ipk)
			listing="$tmp/data.tar.gz.list"
			if [ "$("$TAR" -xOzf "$tmp/control.tar.gz" ./conffiles 2>/dev/null)" != /etc/config/wattline ]; then
				echo "$ipk: /etc/config/wattline is not declared persistent" >&2
				failed=1
			fi
			for path in ./etc/init.d/wattlined ./etc/uci-defaults/99-wattline ./etc/hotplug.d/iface/95-wattline ./usr/lib/wattline/firewall-sync ./usr/lib/wattline/vpn-firewall-repair; do
				if ! awk -v path="$path" '$1 == "-rwxr-xr-x" && $6 == path { found = 1 } END { exit !found }' "$listing"; then
					echo "$ipk: $path is missing or not mode 0755" >&2
					failed=1
				fi
			done
			for path in ./etc/wattline/ ./etc/wattline/tls/; do
				if ! awk -v path="$path" '$1 == "drwx------" && $6 == path { found = 1 } END { exit !found }' "$listing"; then
					echo "$ipk: $path is missing or not mode 0700" >&2
					failed=1
				fi
			done
			if awk '$6 == "./etc/wattline/tls/server.key" || $6 == "./etc/wattline/tokens.json" { found = 1 } END { exit !found }' "$listing"; then
				echo "$ipk: private key or token store must not be shipped" >&2
				failed=1
			fi
			;;
		wattline-rtl8761b_*.ipk)
			control_listing="$tmp/control.tar.gz.list"
			data_listing="$tmp/data.tar.gz.list"
			for path in ./preinst ./postinst ./prerm; do
				if ! awk -v path="$path" '$1 == "-rwxr-xr-x" && $6 == path { found = 1 } END { exit !found }' "$control_listing"; then
					echo "$ipk: CONTROL/${path#./} is missing or not mode 0755" >&2
					failed=1
				fi
			done
			for path in \
				./usr/lib/wattline/rtl8761b/driverctl \
				./etc/init.d/wattline-rtl8761b \
				./etc/hotplug.d/usb/20-wattline-rtl8761b; do
				if ! awk -v path="$path" '$1 == "-rwxr-xr-x" && $6 == path { found = 1 } END { exit !found }' "$data_listing"; then
					echo "$ipk: $path is missing or not mode 0755" >&2
					failed=1
				fi
			done
			for path in \
				./usr/lib/wattline/rtl8761b/modules/5.4.211/btintel.ko \
				./usr/lib/wattline/rtl8761b/modules/5.4.211/btrtl.ko \
				./usr/lib/wattline/rtl8761b/modules/5.4.211/btusb.ko \
				./lib/firmware/rtl_bt/rtl8761bu_fw.bin \
				./lib/firmware/rtl_bt/rtl8761bu_config.bin; do
				if ! awk -v path="$path" '$1 == "-rw-r--r--" && $6 == path { found = 1 } END { exit !found }' "$data_listing"; then
					echo "$ipk: $path is missing or not mode 0644" >&2
					failed=1
				fi
			done
			for name in SHA256SUMS PROVENANCE.md COPYING WHENCE LICENCE.rtlwifi_firmware.txt \
				linux-5.4.211-rtl8761b-gl-abi.patch router-4.8.3.config; do
				path="./usr/share/wattline-rtl8761b/$name"
				if ! awk -v path="$path" '$1 == "-rw-r--r--" && $6 == path { found = 1 } END { exit !found }' "$data_listing"; then
					echo "$ipk: required provenance $path is missing or not mode 0644" >&2
					failed=1
				fi
			done
			;;
	esac
done

exit "$failed"
