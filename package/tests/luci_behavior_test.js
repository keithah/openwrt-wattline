'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const root = path.resolve(__dirname, '..');
function loadModule(name, globals) {
	const source = fs.readFileSync(path.join(root, 'luci-app-wattline/www/luci-static/resources/wattline', name), 'utf8');
	const names = Object.keys(globals || {});
	return Function.apply(null, names.concat(source)).apply(null, names.map((key) => globals[key]));
}

async function transportTests() {
	const calls = [];
	const ok = () => Promise.resolve({ ok: true, json: async () => ({ connected: true }) });
	const responses = [Promise.reject(new Error('transient TLS failure')), ok(), ok(), ok()];
	const transport = loadModule('transport.js').create({
		token: 'admin-secret',
		config: { httpsEnabled: true, httpsPort: 8378, httpEnabled: true, httpPort: 8377 },
		host: 'router.lan',
		fetch: (url, options) => { calls.push([url, options]); return responses.shift(); }
	});
	await transport.json('GET', '/device');
	assert.deepStrictEqual(calls.map((call) => call[0]), [
		'https://router.lan:8378/api/v1/device', 'http://router.lan:8377/api/v1/device'
	], 'safe GET probes HTTPS then HTTP');
	await transport.json('POST', '/pairing-mode');
	await transport.json('GET', '/tokens');
	assert.deepStrictEqual(calls.slice(2).map((call) => call[0]), [
		'https://router.lan:8378/api/v1/pairing-mode',
		'https://router.lan:8378/api/v1/tokens'
	], 'HTTP read fallback never downgrades a mutation or later HTTPS recovery');

	for (const method of ['POST', 'PUT', 'DELETE']) {
		let mutationCalls = 0;
		const ambiguous = loadModule('transport.js').create({
			token: 'admin-secret',
			config: { httpsEnabled: true, httpsPort: 8378, httpEnabled: true, httpPort: 8377 },
			host: 'router.lan',
			fetch: async () => { mutationCalls++; throw new Error('connection reset after write'); }
		});
		await assert.rejects(ambiguous.json(method, '/mutation', { confirm: true }), /connection reset/);
		assert.strictEqual(mutationCalls, 1, method + ' is never replayed on fallback transport');
	}

	let abortCalls = 0;
	const aborted = loadModule('transport.js').create({
		token: 'admin-secret',
		config: { httpsEnabled: true, httpsPort: 8378, httpEnabled: true, httpPort: 8377 },
		host: 'router.lan',
		fetch: async () => { abortCalls++; const error = new Error('aborted'); error.name = 'AbortError'; throw error; }
	});
	const controller = new AbortController();
	controller.abort();
	await assert.rejects(aborted.blob('GET', '/pairing-mode/qr.png', null, { signal: controller.signal }), /aborted/);
	assert.strictEqual(abortCalls, 1, 'an aborted safe request does not probe another listener');

	const httpOnlyCalls = [];
	const httpOnly = loadModule('transport.js').create({
		token: 'admin-secret',
		config: { httpsEnabled: false, httpsPort: 8378, httpEnabled: true, httpPort: 8377 },
		host: 'router.lan', fetch: (url) => { httpOnlyCalls.push(url); return ok(); }
	});
	await httpOnly.json('DELETE', '/pairing-mode');
	assert.deepStrictEqual(httpOnlyCalls, ['http://router.lan:8377/api/v1/pairing-mode'], 'HTTP mutation requires HTTPS to be disabled');
}

async function refreshTests() {
	const rendered = [], loads = [];
	const refresh = loadModule('refresh.js').create({
		load: () => new Promise((resolve) => loads.push(resolve)),
		render: (value) => rendered.push(value)
	});
	const stale = refresh.refresh();
	let finishMutation;
	const mutation = refresh.mutation(() => new Promise((resolve) => { finishMutation = resolve; }));
	await Promise.resolve();
	await refresh.refresh();
	assert.strictEqual(loads.length, 1, 'poll refresh is suppressed while an admin mutation is pending');
	loads[0]('stale pairing/token state');
	await stale;
	assert.deepStrictEqual(rendered, [], 'pre-mutation response cannot overwrite close/revoke state');
	finishMutation('closed');
	await Promise.resolve();
	await Promise.resolve();
	assert.strictEqual(loads.length, 2, 'mutation completion triggers one authoritative refresh');
	loads[1]('authoritative state');
	await mutation;
	assert.deepStrictEqual(rendered, ['authoritative state']);
}

async function qrTests() {
	let pending = [];
	const created = [], revoked = [];
	const qr = loadModule('qr.js', { AbortController }).create({
		fetchBlob: (signal) => new Promise((resolve) => pending.push({ resolve, signal })),
		createObjectURL: (blob) => { const value = 'blob:' + blob.id; created.push(value); return value; },
		revokeObjectURL: (value) => revoked.push(value)
	});
	const image = {
		src: '',
		removeAttribute(name) { if (name === 'src') this.src = ''; }
	};
	const first = qr.load(image, 'first');
	const firstRequest = pending.shift();
	const second = qr.load(image, 'second');
	const secondRequest = pending.shift();
	assert.strictEqual(firstRequest.signal.aborted, true, 'new QR generation aborts old request');
	secondRequest.resolve({ id: 'current' });
	await second;
	firstRequest.resolve({ id: 'stale-pin-bearing' });
	await first;
	assert.strictEqual(image.src, 'blob:current');
	assert.deepStrictEqual(created, ['blob:current'], 'stale completion cannot create or replace an object URL');
	qr.close(image);
	assert.strictEqual(image.src, '');
	assert.deepStrictEqual(revoked, ['blob:current'], 'close revokes the current enrollment QR');

	const afterClose = qr.load(image, 'third');
	const closeRequest = pending.shift();
	qr.close(image);
	assert.strictEqual(closeRequest.signal.aborted, true, 'close aborts in-flight QR fetch');
	closeRequest.resolve({ id: 'closed-pin-bearing' });
	await afterClose;
	assert.deepStrictEqual(created, ['blob:current'], 'completion after close creates no surviving URL');

	let coalescedResolve, coalescedFetches = 0;
	const coalesced = loadModule('qr.js', { AbortController }).create({
		fetchBlob: () => { coalescedFetches++; return new Promise((resolve) => { coalescedResolve = resolve; }); },
		createObjectURL: () => 'blob:coalesced', revokeObjectURL: () => {}
	});
	const oldImage = { src: '', removeAttribute() { this.src = ''; } };
	const newImage = { src: '', removeAttribute() { this.src = ''; } };
	const oldLoad = coalesced.load(oldImage, 'same-expiry');
	const newLoad = coalesced.load(newImage, 'same-expiry');
	assert.strictEqual(coalescedFetches, 1, 'refreshes for one enrollment generation coalesce');
	coalescedResolve({ id: 'same' });
	await Promise.all([oldLoad, newLoad]);
	assert.strictEqual(newImage.src, 'blob:coalesced', 'coalesced completion targets the current DOM image');
}

function validationTests() {
	const validation = loadModule('validation.js');
	const valid = {
		httpEnabled: true, httpAddr4: '0.0.0.0', httpAddr6: '::', httpPort: '8377',
		httpsEnabled: true, httpsAddr4: '0.0.0.0', httpsAddr6: '::', httpsPort: '8378',
		tlsCert: '/etc/wattline/tls/server.crt', tlsKey: '/etc/wattline/tls/server.key',
		tokenStore: '/etc/wattline/tokens.json', pairingTTL: '5m', mdnsEnabled: true,
		mdnsInterfaces: ['br-lan']
	};
	assert.strictEqual(validation.validate(valid), null);
	const cases = [
		['at least one listener', { httpEnabled: false, httpsEnabled: false }, /at least one/i],
		['IPv4 family', { httpAddr4: '::1' }, /IPv4/],
		['IPv6 family', { httpAddr6: '127.0.0.1' }, /IPv6/],
		['IPv4-mapped IPv6', { httpAddr6: '::ffff:192.0.2.1' }, /IPv6/],
		['addressless listener', { httpAddr4: '', httpAddr6: '' }, /needs an IPv4 or IPv6/],
		['listener overlap', { httpsPort: '8377' }, /overlap/],
		['positive Go duration', { pairingTTL: '0s' }, /positive/],
		['sub-nanosecond rounds to zero', { pairingTTL: '0.1ns' }, /positive/],
		['signed duration overflow', { pairingTTL: '9223372036.854776s' }, /positive/],
		['bounded Go duration', { pairingTTL: '999999999999999999999h' }, /positive/],
		['clean path', { tlsKey: '../server.key' }, /clean absolute path/],
		['normalized path', { tlsKey: '/etc/wattline/../server.key' }, /clean absolute path/],
		['enabled mDNS list', { mdnsInterfaces: [] }, /at least one interface/],
		['interface selector', { mdnsInterfaces: ['bad interface'] }, /invalid mDNS interface/i],
		['unscoped link-local', { mdnsInterfaces: ['fe80::1'] }, /invalid mDNS interface/i],
		['duplicate selector', { mdnsInterfaces: ['br-lan', 'br-lan'] }, /duplicate/i]
	];
	for (const [name, patch, expected] of cases) {
		const value = Object.assign({}, valid, patch);
		assert.match(validation.validate(value) || '', expected, name);
	}
	assert.strictEqual(validation.validate(Object.assign({}, valid, { mdnsEnabled: false, mdnsInterfaces: [] })), null);
	assert.strictEqual(validation.validate(Object.assign({}, valid, { pairingTTL: '1h30m' })), null);
	assert.strictEqual(validation.validate(Object.assign({}, valid, { pairingTTL: '.5s' })), null);
	assert.strictEqual(validation.validate(Object.assign({}, valid, { pairingTTL: '+1s' })), null);
	assert.strictEqual(validation.validate(Object.assign({}, valid, { pairingTTL: '9223372036.854775807s' })), null);
	assert.strictEqual(validation.validate(Object.assign({}, valid, { mdnsInterfaces: ['fe80::1%br-lan'] })), null);
}

(async () => {
	await transportTests();
	await qrTests();
	await refreshTests();
	validationTests();
	console.log('LuCI behavior tests passed');
})().catch((error) => { console.error(error); process.exit(1); });
