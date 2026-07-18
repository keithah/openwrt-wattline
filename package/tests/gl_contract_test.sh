#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
VIEW="$ROOT/package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js"
RPC="$ROOT/package/gl-app-wattline/usr/lib/oui-httpd/rpc/wattline"

need() {
	file=$1
	pattern=$2
	message=$3
	grep -Eq "$pattern" "$file" || { printf 'missing: %s\n' "$message" >&2; exit 1; }
}

# Canonical API surface and deliberately separate enrollment flows.
for route in /device /telemetry /pairing/status /pairing/scan /pairing/pair \
	/pairing-mode /pairing-mode/qr.png /tokens /settings /tls/rotate \
	/device/dc /device/usbc/output /device/timers /device/restart \
	/device/shutdown /device/ota/enter /device/advanced/running-mode \
	/device/advanced/ble-pin; do
	need "$VIEW" "'$route'" "canonical route $route"
done
for label in 'Pair Link-Power over BLE' 'Pair an API client' 'Device identity' \
	'Hardware / variant' 'Application firmware' 'OTA bootloader' 'Device ID / MAC' \
	'CID' 'Capabilities' 'Pending commands' 'API clients' 'Revoke' 'MagicDNS' \
	'TLS certificate SHA-256' 'Reachability & TLS' 'Advanced controls'; do
	need "$VIEW" "$label" "$label"
done

# The GL session may receive the bootstrap bearer token for API authorization,
# but the authenticated QR must remain a fetched blob with explicit cleanup.
need "$VIEW" 'URL\.createObjectURL' 'authenticated QR object URL'
need "$VIEW" 'URL\.revokeObjectURL' 'QR object URL cleanup'
need "$VIEW" 'AbortController' 'QR cancellation lifecycle'
need "$VIEW" "method === 'GET'" 'GET-only HTTP fallback policy'
need "$VIEW" 'error\.message' 'canonical JSON error envelope'
if grep -Eq 'qr\.png[^\n]*(token=|pin=)|src[^\n]*token|setAttribute\([^\n]*src[^\n]*token' "$VIEW"; then
	printf 'forbidden: enrollment/bootstrap secret embedded in QR URI/DOM\n' >&2
	exit 1
fi

# Polling is stable and administration mutations are not replayed over HTTP.
need "$VIEW" 'setInterval' 'telemetry polling timer'
need "$VIEW" '2000' 'two-second telemetry polling'
need "$VIEW" 'safe \? endpoints\.slice\(\) : \[endpoints\[0\]\]' 'mutations exactly once on preferred listener'
need "$VIEW" 'document\.activeElement' 'focused input preservation'
need "$VIEW" 'window\.confirm' 'destructive confirmations'
need "$VIEW" 'settingsInfo\.advanced.*features' 'advanced support and policy gate'
need "$VIEW" 'WAN access is insecure — use TLS/VPN' 'WAN warning'
need "$VIEW" 'always available to anyone with the PIN' 'always-on pairing warning'

# RPC exposes only the listener/bootstrap configuration needed by the panel.
for key in token http_enabled port https_enabled https_port; do
	need "$RPC" "$key" "RPC field $key"
done
if grep -Eq 'tls_key|private.?key|token_store' "$RPC"; then
	printf 'forbidden: RPC exposes private storage material\n' >&2
	exit 1
fi

# Syntax smoke tests. Lua is unavailable on some build hosts, so validate it
# when present and always reject common shell/JavaScript contamination.
node --check "$VIEW"
if command -v luac >/dev/null 2>&1; then luac -p "$RPC"; fi
if grep -Eq '\[\[|const[[:space:]]|var[[:space:]]|=>|local[[:space:]]+[^=]+:=' "$RPC"; then
	printf 'forbidden: non-Lua RPC syntax\n' >&2
	exit 1
fi

VIEW="$VIEW" node <<'JS'
'use strict';
const fs = require('fs');
const assert = require('assert');
const source = fs.readFileSync(process.env.VIEW, 'utf8');

function transportFactory(fetchImpl) {
	const start = source.indexOf('  function apiError');
	const end = source.indexOf('  function flow', start);
	assert(start >= 0 && end > start, 'transport helpers remain inspectable');
	return Function('fetch', 'window', source.slice(start, end) + '\nreturn apiClient;')(
		fetchImpl, { location: { hostname: 'router.lan' } });
}

async function transportTests() {
	const calls = [];
	const replies = [Promise.reject(new Error('TLS unavailable')), Promise.resolve({ ok: true, json: async () => ({ ok: true }) })];
	const factory = transportFactory((url, options) => { calls.push([url, options]); return replies.shift(); });
	const client = factory({ https_enabled: true, https_port: 8378, http_enabled: true, port: 8377 }, 'admin-secret');
	await client.json('GET', '/device');
	assert.deepStrictEqual(calls.map((item) => item[0]), [
		'https://router.lan:8378/api/v1/device', 'http://router.lan:8377/api/v1/device'
	], 'safe GET prefers HTTPS then falls back to HTTP');

	let mutations = 0;
	const failing = transportFactory(async (url) => { mutations++; assert.match(url, /^https:/); throw new Error('ambiguous reset'); })(
		{ https_enabled: true, https_port: 8378, http_enabled: true, port: 8377 }, 'admin-secret');
	await assert.rejects(failing.json('PUT', '/settings', { advanced: true }), /ambiguous reset/);
	assert.strictEqual(mutations, 1, 'mutation is never replayed over HTTP');

	const canonical = transportFactory(async () => ({ ok: false, status: 403, json: async () => ({
		error: { code: 'advanced_disabled', message: 'Advanced operations are disabled', details: {} }
	}) }))({ https_enabled: true, https_port: 8378, http_enabled: false, port: 8377 }, 'admin-secret');
	await assert.rejects(canonical.json('GET', '/device/ota'), (error) =>
		error.code === 'advanced_disabled' && error.status === 403 && /disabled/.test(error.message));
}

function componentMethods(environment) {
	return Function('fetch', 'window', 'document', 'URL', 'AbortController', 'return (' + source + '\n);')(
		environment.fetch, environment.window, environment.document, environment.URL, AbortController).methods;
}

async function lifecycleTests() {
	const revoked = [], pending = [];
	const environment = {
		fetch: () => { throw new Error('unused'); }, window: { location: { hostname: 'router.lan' } },
		document: { activeElement: { tagName: 'INPUT' } },
		URL: { createObjectURL: (blob) => 'blob:' + blob.id, revokeObjectURL: (value) => revoked.push(value) }
	};
	const methods = componentMethods(environment);
	const context = {
		qrCtl: null, qrURL: 'blob:old', _qrExpiry: null, adminErr: '',
		client: { blob: (method, path, body, extra) => new Promise((resolve) => pending.push({ resolve, signal: extra.signal })) }
	};
	context.clearQR = methods.clearQR.bind(context);
	context.loadQR = methods.loadQR.bind(context);
	methods.loadQR.call(context, 'first');
	const first = pending.shift();
	methods.loadQR.call(context, 'second');
	const second = pending.shift();
	assert.strictEqual(first.signal.aborted, true, 'new pairing generation aborts stale QR fetch');
	second.resolve({ id: 'current' });
	await new Promise((resolve) => setImmediate(resolve));
	assert.strictEqual(context.qrURL, 'blob:current');
	assert.deepStrictEqual(revoked, ['blob:old'], 'old authenticated QR URL is revoked');
	methods.clearQR.call(context);
	assert.deepStrictEqual(revoked, ['blob:old', 'blob:current'], 'destroy/close revokes current QR URL');

	const values = [
		{ id: 'device', features: {}, commands: { active: [] } },
		{ http: { enabled: true }, https: { enabled: true }, tls: {}, mdns: { interfaces: [] } }, [], { open: false }
	];
	const focused = {
		settingsDraft: { user: 'typing' }, focused: () => true, get: async () => values.shift(),
		loadQR() {}, clearQR() {}, adminErr: '', dev: null, settings: null, tokens: null, apiPair: null
	};
	await methods.fetchAdmin.call(focused);
	assert.deepStrictEqual(focused.settingsDraft, { user: 'typing' }, 'poll cannot rebuild a focused settings form');
}

(async () => {
	await transportTests();
	await lifecycleTests();
	console.log('GL behavior tests passed');
})().catch((error) => { console.error(error); process.exit(1); });
JS

printf 'GL contract tests passed\n'
