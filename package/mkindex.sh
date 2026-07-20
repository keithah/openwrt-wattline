#!/bin/sh
# Generate the GitHub Pages landing page (index.html) + .nojekyll for the opkg
# feed in the output dir. The package list is derived from the .ipk files
# present, so it stays correct without hardcoding versions.
#
# Usage: mkindex.sh <out-dir> [repo-slug]   (repo-slug default: keithah/openwrt-wattline)
set -eu
OUT="${1:-out}"
REPO="${2:-keithah/openwrt-wattline}"
PAGES="https://$(echo "$REPO" | cut -d/ -f1).github.io/$(echo "$REPO" | cut -d/ -f2)"
cd "$OUT"

touch .nojekyll

{
cat <<HTML
<!doctype html><meta charset="utf-8"><title>openwrt-wattline opkg feed</title>
<style>body{font:16px/1.5 -apple-system,Segoe UI,Roboto,sans-serif;max-width:760px;margin:40px auto;padding:0 16px;color:#202124}code,pre{background:#f3f4f6;border-radius:6px}code{padding:1px 5px}pre{padding:12px;overflow:auto}a{color:#1a73e8}</style>
<h1>openwrt-wattline &mdash; opkg feed</h1>
<p>Monitor and control a PeakDo Link-Power power station from an OpenWrt / GL.iNet
router over Bluetooth LE. Project:
<a href="https://github.com/$REPO">github.com/$REPO</a>.</p>
<h2>Register this feed on the router</h2>
<pre>echo 'src/gz keithah https://keithah.github.io/openwrt-starwatch' &gt;&gt; /etc/opkg/customfeeds.conf
opkg update
opkg install wattlined luci-app-wattline gl-app-wattline   # first time (pulls deps)
opkg upgrade wattlined luci-app-wattline gl-app-wattline    # thereafter</pre>
<p>Bluetooth needs a USB BLE dongle on most GL routers &mdash; see the project README.</p>
<h2>Packages</h2>
<ul>
<li><a href="Packages">Packages</a> / <a href="Packages.gz">Packages.gz</a> (feed index)</li>
HTML
for ipk in *.ipk; do
	[ -f "$ipk" ] || continue
	printf '<li><a href="%s">%s</a></li>\n' "$ipk" "$ipk"
done
echo "</ul>"
} > index.html

echo "wrote $OUT/index.html + .nojekyll"
