#!/bin/sh
set -eu

[ "$#" -eq 2 ] || {
	echo "usage: $0 OUT_DIR VERSION" >&2
	exit 2
}

OUT=$1
VERSION=$2
expected="gl-app-wattline_${VERSION}_all.ipk
luci-app-wattline_${VERSION}_all.ipk
wattline-bt_${VERSION}_all.ipk
wattline-rtl8761b_${VERSION}_aarch64_cortex-a53.ipk
wattlined_${VERSION}_aarch64_cortex-a53.ipk"
actual="$(find "$OUT" -maxdepth 1 -type f -name '*.ipk' -exec basename {} \; | sort)"
expected="$(printf '%s\n' "$expected" | sort)"

[ "$actual" = "$expected" ] || {
	printf 'release IPK inventory mismatch\nexpected:\n%s\nactual:\n%s\n' "$expected" "$actual" >&2
	exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' 0 HUP INT TERM
wattlined_ipk="$OUT/wattlined_${VERSION}_aarch64_cortex-a53.ipk"
tar -xzf "$wattlined_ipk" -C "$tmp" ./data.tar.gz
mkdir "$tmp/data"
(umask 022 && tar -xzf "$tmp/data.tar.gz" -C "$tmp/data")

cmp -s "$tmp/data/usr/libexec/wattline-feed-migrate" "$(dirname "$0")/../wattline-feed-migrate.sh" || {
	echo 'packaged Wattline feed migration helper differs from source' >&2
	exit 1
}
[ "$(stat -c %A "$tmp/data/usr/libexec/wattline-feed-migrate")" = -rwxr-xr-x ] || {
	echo 'packaged Wattline feed migration helper is not mode 0755' >&2
	exit 1
}
if tar -tzf "$tmp/data.tar.gz" | grep -Fx './usr/libexec/keithah-feed-migrate' >/dev/null; then
	echo 'wattlined package still owns the legacy feed migration helper' >&2
	exit 1
fi

echo 'Release inventory tests passed'
