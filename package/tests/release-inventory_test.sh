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
tar -xzf "$tmp/data.tar.gz" -C "$tmp/data"

cmp -s "$tmp/data/usr/libexec/wattline-feed-migrate" "$(dirname "$0")/../wattline-feed-migrate.sh" || {
	echo 'packaged Wattline feed migration helper differs from source' >&2
	exit 1
}
helper_mode="$(LC_ALL=C tar -tzvf "$tmp/data.tar.gz" |
	awk '$NF == "./usr/libexec/wattline-feed-migrate" { print $1; found = 1 } END { exit !found }')"
[ "$helper_mode" = -rwxr-xr-x ] || {
	echo 'packaged Wattline feed migration helper is not mode 0755' >&2
	exit 1
}
if tar -tzf "$tmp/data.tar.gz" | grep -Fx './usr/libexec/keithah-feed-migrate' >/dev/null; then
	echo 'wattlined package still owns the legacy feed migration helper' >&2
	exit 1
fi

if [ "${RELEASE_INVENTORY_SKIP_MODE_REGRESSION:-0}" != 1 ]; then
	sh "$(dirname "$0")/release-inventory-mode_test.sh" "$OUT" "$VERSION"
fi

echo 'Release inventory tests passed'
