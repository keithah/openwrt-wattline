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

		if awk 'substr($1, 1, 1) == "d" && $1 != "drwxr-xr-x" { print; bad = 1 } END { exit !bad }' "$listing"; then
			echo "$ipk: $archive contains non-0755 directories" >&2
			failed=1
		fi
	done
done

exit "$failed"
