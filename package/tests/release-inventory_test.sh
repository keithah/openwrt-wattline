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

echo 'Release inventory tests passed'
