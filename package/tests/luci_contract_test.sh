#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
STATUS="$ROOT/package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js"
SETTINGS="$ROOT/package/luci-app-wattline/www/luci-static/resources/view/wattline/settings.js"
ACL="$ROOT/package/luci-app-wattline/usr/share/rpcd/acl.d/luci-app-wattline.json"

need() {
	file=$1
	pattern=$2
	message=$3
	grep -Eq "$pattern" "$file" || { printf 'missing: %s\n' "$message" >&2; exit 1; }
}

# Canonical device/admin API and the two deliberately separate pairing flows.
need "$STATUS" "'/device'" 'canonical device route'
need "$STATUS" '/pairing/status' 'BLE pairing status route'
need "$STATUS" 'Pair Link-Power over BLE' 'BLE pairing label'
need "$STATUS" 'Pair an API client' 'API-client enrollment label'
need "$STATUS" '/pairing-mode' 'API-client pairing-mode route'
need "$STATUS" '/pairing-mode/qr\.png' 'authenticated enrollment QR route'
need "$STATUS" '/tokens' 'token inventory route'
need "$STATUS" 'Revoke' 'token revocation action'

# HTTPS is preferred, HTTP remains an explicit fallback, and API errors use the
# documented canonical envelope rather than dumping response bodies.
need "$STATUS" 'https_enabled' 'HTTPS enablement lookup'
need "$STATUS" 'https_port' 'HTTPS port lookup'
need "$STATUS" 'http_enabled' 'HTTP fallback lookup'
need "$STATUS" 'error\.message' 'canonical JSON error message'
need "$STATUS" 'URL\.createObjectURL' 'authenticated QR object URL'
need "$STATUS" 'URL\.revokeObjectURL' 'QR object URL cleanup'
if grep -Eq 'qr\.png[^\n]*(token=|pin=)|src[^\n]*token|setAttribute\([^\n]*src[^\n]*token' "$STATUS"; then
	printf 'forbidden: enrollment secret embedded in QR URI/DOM\n' >&2
	exit 1
fi

# Cached identity, capability, pending command, reachability, and TLS pinning.
for label in 'Device identity' 'Hardware / variant' 'Application firmware' \
	'OTA bootloader' 'Device ID / MAC' 'CID' 'Capabilities' 'Pending commands' \
	'TLS certificate SHA-256' 'MagicDNS'; do
	need "$STATUS" "$label" "$label"
done
need "$STATUS" 'expires_at' 'pairing TTL countdown'
need "$STATUS" 'last_seen_at' 'token last-seen metadata'

# Every UCI setting in the v1 reachability/security contract is configurable.
for key in http_enabled http_addr4 http_addr6 port https_enabled https_addr4 \
	https_addr6 https_port tls_cert tls_key pairing_ttl pairing_always_on \
	token_store advanced mdns_enabled mdns_interface wan_access pin; do
	need "$SETTINGS" "'$key'" "UCI setting $key"
done
need "$SETTINGS" 'Restart wattlined' 'daemon restart warning'
for warning in 'insecure — use TLS/VPN' 'always available to anyone with the PIN' \
	'Rotate TLS certificate' 'Factory running mode' \
	'Set BLE PIN' 'Enter OTA mode' 'Shut down Link-Power'; do
	need "$STATUS" "$warning" "$warning confirmation/warning"
done

# ACL remains narrowly scoped to UCI and service control needed by this app.
need "$ACL" '"wattline"' 'Wattline UCI ACL'
need "$ACL" '"service"' 'service status ACL'

printf 'LuCI contract tests passed\n'
