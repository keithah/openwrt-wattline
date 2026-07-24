#!/bin/sh
set -eu

[ "$#" -eq 2 ] || {
	echo "usage: $0 OUT_DIR VERSION" >&2
	exit 2
}

OUT=$1
VERSION=$2
package_root="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
inventory_test="$package_root/tests/release-inventory_test.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' 0 HUP INT TERM
mkdir -p "$tmp/out" "$tmp/outer" "$tmp/data"
cp "$OUT"/*.ipk "$tmp/out/"

wattlined_ipk="$tmp/out/wattlined_${VERSION}_aarch64_cortex-a53.ipk"
tar -xzf "$wattlined_ipk" -C "$tmp/outer"
tar -xzf "$tmp/outer/data.tar.gz" -C "$tmp/data"
chmod 0777 "$tmp/data/usr/libexec/wattline-feed-migrate"
tar -czf "$tmp/outer/data.tar.gz" -C "$tmp/data" .
tar -czf "$wattlined_ipk" -C "$tmp/outer" \
	./debian-binary ./control.tar.gz ./data.tar.gz

if output="$(RELEASE_INVENTORY_SKIP_MODE_REGRESSION=1 \
	sh "$inventory_test" "$tmp/out" "$VERSION" 2>&1)"; then
	echo 'release inventory mode test: accepted helper archived with mode 0777' >&2
	exit 1
fi
printf '%s\n' "$output" |
	grep -Fx 'packaged Wattline feed migration helper is not mode 0755' >/dev/null || {
	printf 'release inventory mode test: unexpected rejection:\n%s\n' "$output" >&2
	exit 1
}

echo 'Release inventory mode regression test passed'
