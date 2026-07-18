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
for label in 'One-shot date' 'Weekly repeat mask' 'Monthly repeat mask' 'Enabled' 'Disabled'; do
	need "$VIEW" "$label" "timer editor $label"
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
need "$VIEW" 'advancedCapabilities' 'advanced support and policy gate'
need "$VIEW" "h\('button'" 'native output toggles'
need "$VIEW" 'aria-pressed' 'accessible output toggle state'
need "$VIEW" 'Delete this on-device timer' 'timer deletion confirmation'
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

	let exactRequest;
	const exact = transportFactory(async (url, options) => {
		exactRequest = [url, options]; return { ok: true, json: async () => ({ status: 'restarting' }) };
	})({ https_enabled: true, https_port: 8378, http_enabled: true, port: 8377 }, 'admin-secret');
	await exact.json('POST', '/device/restart');
	assert.strictEqual(exactRequest[1].body, null, 'zero-body lifecycle request has no payload');
	assert.strictEqual(exactRequest[1].headers['Content-Type'], undefined, 'zero-body request has no JSON content type');
}

function componentMethods(environment) {
	return component(environment).methods;
}
function component(environment) {
	return Function('fetch', 'window', 'document', 'URL', 'AbortController', 'return (' + source + '\n);')(
		environment.fetch, environment.window, environment.document, environment.URL, AbortController);
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

async function actionAndTimerTests() {
	const environment = { fetch: () => {}, window: { location: { hostname: 'router.lan' } },
		document: { activeElement: null }, URL: { createObjectURL() {}, revokeObjectURL() {} } };
	const methods = componentMethods(environment);
	const requests = [];
	const action = { ctlErr: '', commandBusy: {}, isPending: () => false,
		post: async (path, body) => { requests.push([path, body]); }, tick() {} };
	methods.act.call(action, 'restart');
	methods.act.call(action, 'shutdown');
	await new Promise((resolve) => setImmediate(resolve));
	assert.deepStrictEqual(requests, [
		['/device/restart', undefined], ['/device/shutdown', { confirm: true }]
	], 'lifecycle actions use the canonical exact request bodies');

	const timerCases = [
		[{ status: 1, type: 0, hour: 6, minute: 30, action: 1, date: '2026-07-18', repeatInput: '' }, 302450666],
		[{ status: -1, type: 1, hour: 7, minute: 2, action: 0, date: '', repeatInput: '' }, 0],
		[{ status: 1, type: 2, hour: 8, minute: 3, action: 1, date: '', repeatInput: '62' }, 62],
		[{ status: -1, type: 3, hour: 9, minute: 4, action: 0, date: '', repeatInput: '2147483650' }, 2147483650]
	];
	for (const [input, repeat] of timerCases) {
		const actual = methods.timerPayload.call({}, input);
		assert.deepStrictEqual(actual, { status: input.status, type: input.type, hour: input.hour,
			minute: input.minute, repeat, action: input.action });
	}
	for (const input of [
		{ status: 0, type: 1, hour: 6, minute: 30, action: 1 },
		{ status: 1, type: 0, hour: 6, minute: 30, action: 1, date: '2026-02-30' },
		{ status: 1, type: 2, hour: 6, minute: 30, action: 1, repeatInput: '1' },
		{ status: 1, type: 3, hour: 6, minute: 30, action: 1, repeatInput: '4294967296' }
	]) assert.throws(() => methods.timerPayload.call({}, input), /timer|date|mask|status/i);
}

async function adminRaceTests() {
	const environment = { fetch: () => {}, window: { location: { hostname: 'router.lan' }, confirm: () => true },
		document: { activeElement: null }, URL: { createObjectURL() {}, revokeObjectURL() {} } };
	const view = component(environment), methods = view.methods;
	const groups = [], mutations = [];
	function deferred() { let resolve; const promise = new Promise((r) => { resolve = r; }); return { promise, resolve }; }
	const context = {
		adminGeneration: 0, adminMutations: 0, destroyed: false, settingsDraft: null, adminErr: '',
		dev: null, settings: null, tokens: [], apiPair: null, qrURL: '', qrCtl: null,
		focused: () => false, loadQR() { this.loadedQR = true; }, clearQR() { this.clearedQR = true; },
		get(path) { const d = deferred(); groups.push([path, d]); return d.promise; },
		mutate() { const d = deferred(); mutations.push(d); return d.promise; }
	};
	context.fetchAdmin = methods.fetchAdmin.bind(context);
	const stale = context.fetchAdmin();
	const closing = methods.adminAction.call(context, 'DELETE', '/pairing-mode', null);
	groups.slice(0, 4).forEach((entry, index) => entry[1].resolve([
		{ id: 'stale' }, { http: {}, https: {}, mdns: {}, tls: {} }, [{ id: 'stale' }], { open: true, expires_at: 'stale' }
	][index]));
	await stale;
	assert.strictEqual(context.dev, null, 'pre-mutation refresh cannot overwrite state');
	assert.strictEqual(context.loadedQR, undefined, 'stale pairing response cannot recreate QR after close');
	mutations[0].resolve({});
	await new Promise((resolve) => setImmediate(resolve));
	groups.slice(4, 8).forEach((entry, index) => entry[1].resolve([
		{ id: 'fresh' }, { http: {}, https: {}, mdns: {}, tls: {} }, [], { open: false }
	][index]));
	await closing;
	assert.strictEqual(context.dev.id, 'fresh', 'mutation completion performs one authoritative refresh');

	const destroyGroup = [];
	context.get = function (path) { const d = deferred(); destroyGroup.push([path, d]); return d.promise; };
	const pending = context.fetchAdmin();
	view.beforeDestroy.call(Object.assign(context, { _iv: null, clearQR: methods.clearQR.bind(context) }));
	destroyGroup.forEach((entry, index) => entry[1].resolve([
		{ id: 'after-destroy' }, { http: {}, https: {}, mdns: {}, tls: {} }, [], { open: true }
	][index]));
	await pending;
	assert.strictEqual(context.dev.id, 'fresh', 'destroyed view ignores late refresh completion');
}

function capabilityTests() {
	const start = source.indexOf('  function advancedCapabilities');
	const end = source.indexOf('  function flow', start);
	assert(start >= 0 && end > start, 'advanced capability policy helper exists');
	const capabilities = Function(source.slice(start, end) + '\nreturn advancedCapabilities;')();
	const keys = ['ota', 'clock', 'runningMode', 'barrierFree', 'usbFirmware', 'blePIN'];
	const fixtures = {
		ota: { available: { ota: true }, features: {} }, clock: { available: { current_time: true }, features: {} },
		runningMode: { available: {}, features: { running_mode: true } }, barrierFree: { available: {}, features: { barrier_free: true } },
		usbFirmware: { available: {}, features: { usb_firmware: true } }, blePIN: { available: {}, features: { ble_pin: true } }
	};
	for (const key of keys) {
		const actual = capabilities({ advanced: true }, fixtures[key]);
		for (const candidate of keys) assert.strictEqual(actual[candidate], candidate === key, key + ' capability is isolated');
		assert.deepStrictEqual(capabilities({ advanced: false }, fixtures[key]), {
			ota: false, clock: false, runningMode: false, barrierFree: false, usbFirmware: false, blePIN: false
		}, 'administrative flag gates ' + key);
	}
}

function timerPresentationTests() {
	const start = source.indexOf('  function advancedCapabilities');
	const end = source.indexOf('  function flow', start);
	const helpers = Function(source.slice(start, end) + '\nreturn { input: timerInputKind, status: timerStatusLabel, action: timerActionLabel };')();
	assert.deepStrictEqual([0, 1, 2, 3].map(helpers.input), ['date', 'none', 'weekly_mask', 'monthly_mask']);
	assert.deepStrictEqual([1, -1, -2, -3].map(helpers.status), ['enabled', 'disabled', 'validation-disabled', 'expired']);
	assert.deepStrictEqual([0, 1].map(helpers.action), ['Off', 'On']);
}

(async () => {
	await transportTests();
	await lifecycleTests();
	await actionAndTimerTests();
	await adminRaceTests();
	capabilityTests();
	timerPresentationTests();
	console.log('GL behavior tests passed');
})().catch((error) => { console.error(error); process.exit(1); });
JS

printf 'GL contract tests passed\n'
