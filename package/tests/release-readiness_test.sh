#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
WORKFLOW="$ROOT/.github/workflows/release.yml"

assert_fixed() {
	needle=$1
	file=$2
	grep -Fqx "$needle" "$file" || {
		echo "missing exact line in ${file#"$ROOT/"}: $needle" >&2
		exit 1
	}
}

assert_contains() {
	needle=$1
	file=$2
	grep -Fq -- "$needle" "$file" || {
		echo "missing text in ${file#"$ROOT/"}: $needle" >&2
		exit 1
	}
}

assert_fixed 'VERSION := 0.1.4' "$ROOT/package/Makefile"
for control in \
	package/gl-app-wattline/CONTROL/control \
	package/luci-app-wattline/CONTROL/control \
	package/wattline-bt/CONTROL/control \
	package/wattline-rtl8761b/CONTROL/control \
	package/wattlined/CONTROL/control; do
	assert_fixed 'Version: 0.1.4' "$ROOT/$control"
done

assert_contains 'uses: actions/checkout@v7' "$WORKFLOW"
assert_contains 'uses: actions/setup-go@v6' "$WORKFLOW"
assert_contains 'uses: actions/setup-node@v6' "$WORKFLOW"
assert_contains 'make -C package VERSION=${{ steps.ver.outputs.version }} all' "$WORKFLOW"
assert_contains 'gh release view "$tag" --json assets' "$WORKFLOW"
assert_contains 'gh release download "$tag"' "$WORKFLOW"
assert_contains 'cmp -- "package/out/$asset" "$existing_dir/$asset"' "$WORKFLOW"
assert_contains 'gh release create "$tag"' "$WORKFLOW"
assert_contains "--sort=name --mtime='@0'" "$ROOT/package/Makefile"
assert_contains 'gzip -9n -c' "$ROOT/package/Makefile"

if grep -Eq 'package/out/(Packages|Packages\.gz|Packages\.sig|.*\.pub)' "$WORKFLOW"; then
	echo 'release workflow must not publish shared feed metadata or keys' >&2
	exit 1
fi

assert_contains 'https://keithah.github.io/openwrt-packages/install-wattline.sh' "$ROOT/README.md"
assert_contains 'automatically migrates' "$ROOT/README.md"

echo 'Release readiness tests passed'
