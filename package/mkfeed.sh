#!/bin/sh
# Generate an opkg feed index (Packages + Packages.gz) for the .ipk files in
# the output dir, so a router can `opkg update && opkg upgrade` instead of
# needing a manual reinstall. Portable across macOS (BSD) and Linux (GNU).
#
# Usage: mkfeed.sh <out-dir>
set -eu
OUT="${1:-out}"
cd "$OUT"

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }
fsize()  { if stat -f%z "$1" >/dev/null 2>&1; then stat -f%z "$1"; else stat -c%s "$1"; fi; }

: > Packages
for ipk in *.ipk; do
	[ -f "$ipk" ] || continue
	# control.tar.gz is a member of the outer gzip-tar; extract its control file.
	ctrl="$(tar xzOf "$ipk" ./control.tar.gz 2>/dev/null | tar xzO ./control 2>/dev/null)"
	# strip any trailing blank lines from the control block
	printf '%s\n' "$ctrl" | sed -e '/^[[:space:]]*$/d' >> Packages
	printf 'Filename: %s\n' "$ipk" >> Packages
	printf 'Size: %s\n' "$(fsize "$ipk")" >> Packages
	printf 'SHA256sum: %s\n' "$(sha256 "$ipk")" >> Packages
	printf '\n' >> Packages
done

gzip -9 -c Packages > Packages.gz
echo "wrote $OUT/Packages and $OUT/Packages.gz ($(grep -c '^Package:' Packages) packages)"
