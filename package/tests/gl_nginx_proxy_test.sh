#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
FRAGMENT="$ROOT/package/gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations"
POSTINST="$ROOT/package/gl-app-wattline/CONTROL/postinst"
PRERM="$ROOT/package/gl-app-wattline/CONTROL/prerm"
MAKEFILE="$ROOT/package/Makefile"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

need() {
	file=$1 pattern=$2 message=$3
	grep -Fq "$pattern" "$file" || { echo "missing: $message" >&2; exit 1; }
}

need "$FRAGMENT" 'location = /wattline {' 'canonical-prefix redirect'
need "$FRAGMENT" 'return 308 /wattline/;' 'canonical-prefix redirect target'
need "$FRAGMENT" 'location ^~ /wattline/ {' 'stable Wattline prefix'
need "$FRAGMENT" 'proxy_pass http://127.0.0.1:8377/api/v1/;' 'v1 upstream rewrite'
need "$FRAGMENT" 'proxy_http_version 1.1;' 'SSE HTTP version'
need "$FRAGMENT" 'proxy_set_header Authorization $http_authorization;' 'bearer forwarding'
need "$FRAGMENT" 'proxy_set_header Connection "";' 'persistent SSE upstream'
need "$FRAGMENT" 'proxy_buffering off;' 'SSE buffering disabled'
need "$FRAGMENT" 'proxy_cache off;' 'SSE cache disabled'
need "$FRAGMENT" 'proxy_read_timeout 1h;' 'long-lived SSE timeout'
need "$MAKEFILE" 'gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations' 'fragment staging'
need "$MAKEFILE" 'stage-gl/CONTROL/prerm' 'prerm executable mode'

mkdir -p "$TMP/conf.d" "$TMP/gl-conf.d" "$TMP/bin"
cp "$FRAGMENT" "$TMP/conf.d/gl-app-wattline.locations"
CALLS="$TMP/calls"; export CALLS
cat > "$TMP/bin/nginx" <<'EOF'
#!/bin/sh
echo "nginx $*" >> "$CALLS"
[ "${FAIL_NGINX_TEST:-0}" != 1 ]
EOF
cat > "$TMP/bin/nginx-init" <<'EOF'
#!/bin/sh
echo "init $*" >> "$CALLS"
EOF
chmod +x "$TMP/bin/nginx" "$TMP/bin/nginx-init"

export WATTLINE_NGINX_SOURCE="$TMP/conf.d/gl-app-wattline.locations"
export WATTLINE_NGINX_LINK="$TMP/gl-conf.d/gl-app-wattline.conf"
export WATTLINE_NGINX_BIN="$TMP/bin/nginx"
export WATTLINE_NGINX_INIT="$TMP/bin/nginx-init"

sh "$POSTINST"
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$WATTLINE_NGINX_SOURCE" ]
grep -Fqx 'nginx -t' "$CALLS"
grep -Fqx 'init reload' "$CALLS"
sh "$POSTINST"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 2 ]

sh "$PRERM"
[ ! -e "$WATTLINE_NGINX_LINK" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 3 ]
sh "$PRERM"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 3 ]

echo unrelated > "$WATTLINE_NGINX_LINK"
if sh "$POSTINST"; then echo 'postinst overwrote an unrelated file' >&2; exit 1; fi
grep -Fqx unrelated "$WATTLINE_NGINX_LINK"
rm "$WATTLINE_NGINX_LINK"
ln -s "$TMP/conf.d/other.locations" "$WATTLINE_NGINX_LINK"
if sh "$POSTINST"; then echo 'postinst overwrote an unrelated symlink' >&2; exit 1; fi
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$TMP/conf.d/other.locations" ]
rm "$WATTLINE_NGINX_LINK"

FAIL_NGINX_TEST=1; export FAIL_NGINX_TEST
before_reload=$(grep -Fc 'init reload' "$CALLS")
if sh "$POSTINST"; then echo 'postinst accepted invalid nginx config' >&2; exit 1; fi
[ ! -e "$WATTLINE_NGINX_LINK" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$before_reload" ]
unset FAIL_NGINX_TEST

sh "$POSTINST"
FAIL_NGINX_TEST=1; export FAIL_NGINX_TEST
if sh "$PRERM"; then echo 'prerm accepted invalid remaining nginx config' >&2; exit 1; fi
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$WATTLINE_NGINX_SOURCE" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$((before_reload + 1))" ]
unset FAIL_NGINX_TEST

IPKG_INSTROOT="$TMP/root" sh "$POSTINST"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$((before_reload + 1))" ]

echo 'GL nginx proxy tests passed'
