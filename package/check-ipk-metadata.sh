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

	case "$(basename "$ipk")" in
		wattlined_*.ipk)
			listing="$tmp/data.tar.gz.list"
			if [ "$("$TAR" -xOzf "$tmp/control.tar.gz" ./conffiles 2>/dev/null)" != /etc/config/wattline ]; then
				echo "$ipk: /etc/config/wattline is not declared persistent" >&2
				failed=1
			fi
			for path in ./etc/init.d/wattlined ./etc/uci-defaults/99-wattline ./etc/hotplug.d/iface/95-wattline ./usr/lib/wattline/firewall-sync; do
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
	esac
done

exit "$failed"
