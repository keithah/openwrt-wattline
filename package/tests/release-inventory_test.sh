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

[ -s "$OUT/Packages" ] || {
	echo 'missing Packages feed index' >&2
	exit 1
}
[ -s "$OUT/Packages.gz" ] || {
	echo 'missing Packages.gz feed index' >&2
	exit 1
}
[ "$(grep -c '^Package:' "$OUT/Packages")" -eq 5 ] || {
	echo 'feed index does not contain exactly five packages' >&2
	exit 1
}
for package in wattlined wattline-bt wattline-rtl8761b luci-app-wattline gl-app-wattline; do
	[ "$(grep -c "^Package: $package$" "$OUT/Packages")" -eq 1 ] || {
		echo "feed index is missing or duplicates $package" >&2
		exit 1
	}
done

echo 'Release inventory tests passed'
