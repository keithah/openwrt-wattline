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

class FakeElement {
	constructor(tag, attrs, children, document) {
		this.tagName = tag;
		this.attributes = {};
		this.children = [];
		this.listeners = {};
		this.parentNode = null;
		this.ownerDocument = document;
		this._text = '';
		for (const [name, value] of Object.entries(attrs || {})) this.setAttribute(name, value);
		this.appendChild(children);
	}
	appendChild(child) {
		if (Array.isArray(child)) { child.forEach((item) => this.appendChild(item)); return child; }
		if (child == null || child === '') return child;
		if (child instanceof FakeElement) {
			if (child.parentNode) child.parentNode.children = child.parentNode.children.filter((item) => item !== child);
			child.parentNode = this;
		}
		this.children.push(child);
		return child;
	}
	replaceChild(next, old) {
		const index = this.children.indexOf(old);
		if (index !== -1) { this.children[index] = next; next.parentNode = this; old.parentNode = null; }
		return old;
	}
	replaceWith(next) { if (this.parentNode) this.parentNode.replaceChild(next, this); }
	setAttribute(name, value) {
		this.attributes[name] = value === '' ? '' : String(value);
		if (name === 'class') this.className = String(value);
		if (name === 'value') this.value = String(value);
		if (name === 'id') this.id = String(value);
		if (name === 'checked') this.checked = true;
		if (name === 'disabled') this.disabled = true;
		if (name === 'src') this.src = String(value);
	}
	removeAttribute(name) {
		delete this.attributes[name];
		if (name === 'disabled') this.disabled = false;
		if (name === 'src') this.src = '';
	}
	addEventListener(name, listener) { (this.listeners[name] ||= []).push(listener); }
	dispatch(name) {
		if (name === 'focus') this.ownerDocument.activeElement = this;
		if (name === 'blur' && this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null;
		for (const listener of this.listeners[name] || []) listener({ target: this, currentTarget: this });
	}
	click() {
		if (this.disabled) return;
		var focused = this.ownerDocument.activeElement;
		if (focused && focused !== this) focused.dispatch('blur');
		this.ownerDocument.activeElement = this;
		this.dispatch('click');
	}
	get innerHTML() { return this.children.map(textOf).join(''); }
	set innerHTML(value) {
		this.children.forEach((child) => { if (child instanceof FakeElement) child.parentNode = null; });
		this.children = [];
		this._text = String(value || '');
	}
	get textContent() { return this._text + this.children.map(textOf).join(''); }
	set textContent(value) { this.children = []; this._text = String(value == null ? '' : value); }
}

function textOf(node) {
	return node instanceof FakeElement ? node.textContent : String(node == null ? '' : node);
}

function descendants(node) {
	const result = [];
	if (!(node instanceof FakeElement)) return result;
	result.push(node);
	for (const child of node.children) result.push(...descendants(child));
	return result;
}

function findElement(rootNode, tag, text) {
	return descendants(rootNode).find((node) => node.tagName === tag &&
		(text == null || textOf(node).trim() === text));
}

function liveText(rootNode) {
	const live = descendants(rootNode).find((node) => node.attributes['aria-live'] === 'polite');
	return live ? textOf(live) : '';
}

async function settle() {
	for (let i = 0; i < 12; i++) await Promise.resolve();
}

async function renderStatus(options = {}) {
	const fixture = require('./power_loss_preset.json');
	const document = { activeElement: null };
	const pollEntries = [];
	const requests = [];
	let getRequests = 0;
	let resolveMutation = null;
	let confirmCalls = 0;
	let mutationFailure = options.mutationFailure || null;
	const failures = {};
	const server = {
		rules: (options.rules || [fixture.canonical]).map((rule) => Object.assign({}, rule)),
		runtime: options.runtime || [{ name: fixture.name, armed: true, holding_for: '5m0s' }],
		telemetry: options.telemetry || {
		connected: true,
		battery: { status: 0, level: 50, wh: 10, max_wh: 20, remain_min: 100, volts: 12 },
		dc: { enabled: true, status: 0, watts: 0, volts: 12, amps: 0 },
		typec: { mode: 0, status: 0, watts: 0, volts: 0, amps: 0, temp_c: 25, dc_input: false }
		},
		device: options.device || { features: {}, commands: { active: [] }, connection: {} },
		settings: options.settings || { tls: {}, pairing_always_on: false, wan_access: false, advanced: false },
		tokens: options.tokens || [],
		pairing: options.pairing || { open: false },
		devicePairing: options.devicePairing || { stage: 'idle', devices: [] }
	};
	let qrLoads = 0, qrCloses = 0;
	const clone = (value) => value == null ? value : JSON.parse(JSON.stringify(value));
	const client = {
		json(method, route, body) {
			if (method !== 'GET') requests.push([method, route, clone(body)]);
			if (method === 'GET') {
				getRequests++;
				if (failures[route]) {
					const error = failures[route];
					delete failures[route];
					return Promise.reject(error);
				}
				const values = {
					'/telemetry': server.telemetry,
					'/status': { device: {}, rules: server.runtime },
					'/device': server.device,
					'/settings': server.settings,
					'/tokens': server.tokens, '/pairing-mode': server.pairing, '/rules': server.rules,
					'/device/usbc-limit': { output: { watts: 30 } },
					'/device/bypass-threshold': { volts: 12 }, '/device/schedules': [],
					'/pairing/status': server.devicePairing
				};
				return Promise.resolve(clone(values[route]));
			}
			if (mutationFailure) {
				const error = mutationFailure;
				mutationFailure = null;
				return Promise.reject(error);
			}
			const save = () => {
				if (route === '/rules') server.rules.push(clone(body));
				else if (route === '/rules/no_input_shutdown') server.rules = server.rules.map((rule) =>
					rule.name === fixture.name ? clone(body) : rule);
				return {};
			};
			if (!options.deferMutation) return Promise.resolve(save());
			return new Promise((resolve) => { resolveMutation = () => resolve(save()); });
		},
		blob() { return Promise.resolve({}); }
	};
	const E = (tag, attrs, children) => new FakeElement(tag, attrs, children, document);
	const globals = {
		view: { extend: (value) => value },
		uci: { load: () => Promise.resolve(), get: (_pkg, _section, key) => key === 'token' ? 'secret' : undefined },
		poll: { add: (callback, seconds) => pollEntries.push({ callback, seconds }) },
		wattlineTransport: { create: () => client },
		wattlineQR: { create: () => ({
			load: (image) => { qrLoads++; image.src = 'blob:qr-' + qrLoads; return Promise.resolve(); },
			close: (image) => { qrCloses++; if (image) image.src = ''; }
		}) },
		wattlineRefresh: loadModule('refresh.js'),
		wattlinePowerLoss: loadModule('power_loss.js'),
		wattlinePairingProgress: loadModule('pairing_progress.js'),
		E, _: (value) => value, document,
		window: {
			location: { hostname: 'router.test' }, addEventListener() {}, alert() {}, prompt() { return null; },
			confirm() { confirmCalls++; return true; }
		},
		fetch() {}, URL: { createObjectURL: () => 'blob:test', revokeObjectURL() {} }
	};
	const source = fs.readFileSync(path.join(root,
		'luci-app-wattline/www/luci-static/resources/view/wattline/status.js'), 'utf8');
	const names = Object.keys(globals);
	const view = Function.apply(null, names.concat(source)).apply(null, names.map((key) => globals[key]));
	const dom = view.render();
	await settle();
	return {
		dom, requests, pollEntries,
		getCount: () => getRequests,
		confirmCalls: () => confirmCalls,
		qrCounts: () => [qrLoads, qrCloses],
		setServer: (patch) => Object.assign(server, patch),
		failNext: (route, error) => { failures[route] = error; },
		resolveMutation: () => { assert.ok(resolveMutation, 'a mutation is pending'); resolveMutation(); },
		refreshAdmin: async () => { await pollEntries.find((entry) => entry.seconds === 10).callback(); await settle(); }
	};
}

async function pairingProgressTests() {
	const pairing = loadModule('pairing_progress.js');
	const status = {
		stage: 'pairing', phase: 'confirming_bond', message: 'Confirming the replacement bond', elapsed_ms: 17000,
		target: 'DC:04:5A:EB:72:2B', events: [
			{ at: '2026-07-18T23:40:00Z', phase: 'preparing_adapter', message: 'Preparing the Bluetooth adapter' },
			{ at: '2026-07-18T23:40:01Z', phase: 'clearing_stale_bond', message: "Clearing the router's stale pairing record" },
			{ at: '2026-07-18T23:40:02Z', phase: 'locating_device', message: 'Locating Link-Power' },
			{ at: '2026-07-18T23:40:03Z', phase: 'exchanging_pin', message: 'Exchanging the PIN' },
			{ at: '2026-07-18T23:40:04Z', phase: 'confirming_bond', message: 'Confirming the replacement bond' }
		]
	};
	const model = pairing.model(status);
	assert.deepStrictEqual(model.steps.map((step) => step.label), [
		'Preparing adapter', 'Clearing stale router bond', 'Locating device', 'Exchanging PIN',
		'Confirming bond', 'Trusting device', 'Reconnecting', 'Verifying handshake', 'Saved'
	]);
	assert.deepStrictEqual(model.steps.map((step) => step.state), [
		'complete', 'complete', 'complete', 'complete', 'active', 'pending', 'pending', 'pending', 'pending'
	]);
	assert.strictEqual(model.elapsed, '17s');
	assert.strictEqual(JSON.stringify(model.events).includes('020555'), false);

	const failedStatus = Object.assign({}, status, { stage: 'error', phase: 'failed', message: '', error: 'replacement bond rejected',
		events: status.events.concat([{ phase: 'failed', message: 'replacement bond rejected' }]) });
	assert.strictEqual(pairing.model(failedStatus).steps.find((step) => step.phase === 'confirming_bond').state, 'failed');

	const rendered = await renderStatus({
		telemetry: { connected: false }, devicePairing: failedStatus
	});
	for (const label of model.steps.map((step) => step.label))
		assert.ok(textOf(rendered.dom).includes(label), 'renders pairing step ' + label);
	const live = descendants(rendered.dom).find((node) => node.attributes['aria-live'] === 'polite' &&
		textOf(node).includes('replacement bond rejected'));
	assert.ok(live, 'current pairing failure is the only live progress message');
	assert.ok(findElement(rendered.dom, 'summary', 'Technical details'));
	const recover = findElement(rendered.dom, 'button', 'Clear stale pairing & retry');
	assert.ok(recover, 'failed pairing offers explicit recovery');
	recover.click();
	await settle();
	assert.strictEqual(rendered.confirmCalls(), 1);
	assert.deepStrictEqual(rendered.requests[rendered.requests.length - 1], [
		'POST', '/pairing/recover', { mac: status.target, pin: '020555' }
	]);
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

async function powerLossCardTests() {
	const fixture = require('./power_loss_preset.json');
	const powerLoss = loadModule('power_loss.js');
	const customized = Object.assign({}, fixture.canonical, {
		actions: ['webhook:https://example.test/input-lost', 'shutdown'],
		repeat_every: 1800000000000
	});
	const compatible = await renderStatus({ rules: [customized], deferMutation: true });
	const title = findElement(compatible.dom, 'div', 'Power-loss shutdown');
	assert.ok(title, 'renders the dedicated power-loss card');
	assert.ok(descendants(compatible.dom).some((node) => textOf(node).includes(
		'Shutting down Link-Power also powers off this router. It returns only when Link-Power wakes after input power comes back.')),
	'hardware wake limitation is visible');
	const enableLabel = descendants(compatible.dom).find((node) => node.tagName === 'label' && textOf(node).includes('Enable'));
	assert.ok(enableLabel, 'enable checkbox has a visible label');
	const delayLabel = descendants(compatible.dom).find((node) => node.tagName === 'label' && textOf(node).includes('Delay'));
	assert.ok(delayLabel, 'delay input has a visible label');
	let delayInput = descendants(compatible.dom).find((node) => node.tagName === 'input' && node.attributes.type === 'number');
	assert.deepStrictEqual([delayInput.attributes.min, delayInput.attributes.max, delayInput.attributes.step], ['1', '1440', '1']);
	const live = descendants(compatible.dom).find((node) => node.attributes['aria-live'] === 'polite');
	assert.ok(live && textOf(live).includes('5 min remaining'), 'live status comes from runtime countdown state');
	assert.ok(!/shutdown succeeded/i.test(textOf(live)), 'runtime copy does not overstate action success');

	delayInput.dispatch('focus');
	delayInput.value = '17';
	delayInput.dispatch('input');
	const focusedDelayInput = delayInput;
	await compatible.refreshAdmin();
	delayInput = descendants(compatible.dom).find((node) => node.tagName === 'input' && node.attributes.type === 'number');
	assert.ok(delayInput === focusedDelayInput, 'poll keeps the focused delay control mounted');
	assert.strictEqual(delayInput.value, '17', 'poll does not overwrite focused draft');
	const saveButton = findElement(compatible.dom, 'button', 'Save');
	saveButton.click();
	assert.strictEqual(saveButton.disabled, true, 'save is disabled while one mutation is pending');
	await settle();
	const readsBeforePendingPoll = compatible.getCount();
	await compatible.refreshAdmin();
	assert.strictEqual(compatible.getCount(), readsBeforePendingPoll,
		'admin poll does not reload settings, tokens, pairing, or rules during a mutation');
	const preservedPayload = powerLoss.payload(customized, true, 17, false);
	const lastRequest = compatible.requests[compatible.requests.length - 1];
	assert.deepStrictEqual(lastRequest, ['PUT', '/rules/no_input_shutdown', preservedPayload]);
	assert.strictEqual(compatible.confirmCalls(), 0, 'compatible update does not ask for reset confirmation');
	compatible.resolveMutation();
	await settle();

	const reconcile = await renderStatus({ rules: [customized] });
	let reconciledDelay = descendants(reconcile.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'number');
	reconciledDelay.dispatch('focus');
	reconciledDelay.value = '17';
	reconciledDelay.dispatch('input');
	const incompatible = {
		name: fixture.name, enabled: false, condition: 'schedule', actions: ['shutdown'], cron: '0 1 * * *'
	};
	reconcile.setServer({
		rules: [customized],
		runtime: [{ name: fixture.name, armed: false, last_fired: '2026-07-18T12:00:00Z' }],
		device: { model: 'Fresh model', features: {}, commands: { active: [] }, connection: {} },
		settings: { tls: {}, pairing_always_on: true, wan_access: true, advanced: false },
		tokens: [{ id: 'fresh', label: 'Fresh API client', bootstrap: false }],
		pairing: { open: true, pin: '654321', expires_at: '2099-07-18T12:00:00Z' }
	});
	await reconcile.refreshAdmin();
	const delayAfterReconcile = descendants(reconcile.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'number');
	assert.ok(delayAfterReconcile === reconciledDelay,
		'fresh admin state preserves only the focused delay DOM node');
	assert.strictEqual(delayAfterReconcile.value, '17');
	assert.ok(textOf(reconcile.dom).includes('Fresh model'), 'fresh device state renders while delay is focused');
	assert.ok(textOf(reconcile.dom).includes('Fresh API client'), 'fresh token state renders while delay is focused');
	assert.ok(textOf(reconcile.dom).includes('654321'), 'fresh pairing state renders while delay is focused');
	assert.ok(textOf(reconcile.dom).includes('Pairing is always available'),
		'fresh settings state renders while delay is focused');
	assert.match(liveText(reconcile.dom), /Rule last fired/, 'fresh runtime status renders while delay is focused');
	reconcile.setServer({ rules: [incompatible] });
	await reconcile.refreshAdmin();
	assert.ok(descendants(reconcile.dom).includes(reconciledDelay));
	assert.strictEqual(reconciledDelay.value, '17');
	assert.ok(textOf(reconcile.dom).includes('Customized rule conflict'),
		'fresh rules are classified while delay is focused');
	const reconciledEnabled = descendants(reconcile.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'checkbox');
	assert.strictEqual(reconciledEnabled.checked, false,
		'only the focused delay value is preserved; enabled state reconciles from the server');
	const staleSave = findElement(reconcile.dom, 'button', 'Save');
	assert.strictEqual(staleSave.disabled, true, 'fresh conflict disables ordinary Save');
	staleSave.click();
	assert.strictEqual(reconcile.requests.length, 0, 'disabled Save cannot overwrite a newly conflicting rule');
	findElement(reconcile.dom, 'button', 'Reset preset').click();
	await settle();
	assert.strictEqual(reconcile.confirmCalls(), 1);
	assert.deepStrictEqual(reconcile.requests[0], [
		'PUT', '/rules/no_input_shutdown', powerLoss.payload(incompatible, false, 17, true)
	]);

	const rejected = await renderStatus({
		rules: [fixture.canonical], mutationFailure: new Error('preset write rejected')
	});
	const rejectedEnabled = descendants(rejected.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'checkbox');
	rejectedEnabled.checked = false;
	rejectedEnabled.dispatch('change');
	let rejectedDelay = descendants(rejected.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'number');
	rejectedDelay.dispatch('focus');
	rejectedDelay.value = '17';
	rejectedDelay.dispatch('input');
	const readsBeforeRejectedSave = rejected.getCount();
	findElement(rejected.dom, 'button', 'Save').click();
	await settle();
	rejectedDelay = descendants(rejected.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'number');
	const enabledAfterRejection = descendants(rejected.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'checkbox');
	assert.ok(rejected.getCount() > readsBeforeRejectedSave,
		'rejected mutation still performs the coordinator authoritative refresh');
	assert.strictEqual(rejectedDelay.value, '17', 'rejected mutation restores submitted delay draft');
	assert.strictEqual(enabledAfterRejection.checked, false, 'rejected mutation restores submitted enabled draft');
	assert.ok(textOf(rejected.dom).includes('preset write rejected'), 'rejection is visible beside restored draft');

	const missingState = await renderStatus({ rules: [] });
	assert.match(liveText(missingState.dom), /Preset not configured/i);
	assert.doesNotMatch(liveText(missingState.dom), /min remaining/i,
		'missing preset never announces a countdown');
	const disabledRule = Object.assign({}, fixture.canonical, { enabled: false });
	const disabledState = await renderStatus({ rules: [disabledRule] });
	assert.match(liveText(disabledState.dom), /Rule disabled/i);
	assert.doesNotMatch(liveText(disabledState.dom), /min remaining/i,
		'disabled preset never announces a countdown');
	const conflictState = await renderStatus({ rules: [incompatible] });
	assert.match(liveText(conflictState.dom), /Customized rule conflict/i);
	assert.doesNotMatch(liveText(conflictState.dom), /min remaining/i,
		'conflicting preset never announces a countdown');
	const incoherentState = await renderStatus({ rules: [fixture.canonical], runtime: [] });
	assert.match(liveText(incoherentState.dom), /countdown not active/i);
	assert.doesNotMatch(liveText(incoherentState.dom), /min remaining/i,
		'countdown requires coherent runtime status');

	const transient = await renderStatus({
		rules: [fixture.canonical],
		pairing: { open: true, pin: '123456', expires_at: '2099-07-18T12:00:00Z' }
	});
	const transientDelay = descendants(transient.dom).find((node) =>
		node.tagName === 'input' && node.attributes.type === 'number');
	transientDelay.dispatch('focus');
	transientDelay.value = '17';
	transientDelay.dispatch('input');
	const qrImage = descendants(transient.dom).find((node) => node.tagName === 'img');
	const qrSource = qrImage.src;
	const qrCounts = transient.qrCounts();
	for (const route of ['/rules', '/status']) {
		transient.failNext(route, new Error(route + ' temporarily unavailable'));
		await transient.refreshAdmin();
		assert.ok(descendants(transient.dom).includes(transientDelay),
			route + ' failure keeps the focused draft DOM mounted');
		assert.strictEqual(transientDelay.value, '17');
		assert.ok(descendants(transient.dom).includes(qrImage),
			route + ' failure keeps the existing enrollment image mounted');
		assert.strictEqual(qrImage.src, qrSource, route + ' failure keeps the current QR object URL coherent');
		assert.deepStrictEqual(transient.qrCounts(), qrCounts,
			route + ' failure neither reloads nor closes the existing QR generation');
		assert.ok(textOf(transient.dom).includes(route + ' temporarily unavailable'),
			route + ' failure surfaces a non-destructive admin error');
	}

	const missing = await renderStatus({ rules: [] });
	findElement(missing.dom, 'button', 'Save').click();
	await settle();
	assert.deepStrictEqual(missing.requests[missing.requests.length - 1], ['POST', '/rules', fixture.canonical]);

	const conflict = await renderStatus({
		rules: [{ name: fixture.name, condition: 'schedule', actions: ['shutdown'], cron: '0 1 * * *' }],
		deferMutation: true
	});
	const conflictSaveButton = findElement(conflict.dom, 'button', 'Save');
	assert.strictEqual(conflictSaveButton.disabled, true,
		'ordinary save is disabled for an incompatible reserved rule');
	const resetButton = findElement(conflict.dom, 'button', 'Reset preset');
	assert.ok(resetButton, 'conflict exposes a separate reset action');
	resetButton.click();
	await settle();
	assert.strictEqual(conflictSaveButton.disabled, true, 'save stays disabled during reset');
	assert.strictEqual(resetButton.disabled, true, 'reset is disabled while its mutation is pending');
	assert.strictEqual(conflict.confirmCalls(), 1, 'incompatible reset is confirmed exactly once');
	assert.deepStrictEqual(conflict.requests[conflict.requests.length - 1], [
		'PUT', '/rules/no_input_shutdown', fixture.canonical
	]);
	conflict.resolveMutation();
	await settle();
}

(async () => {
	await transportTests();
	await qrTests();
	await refreshTests();
	await pairingProgressTests();
	await powerLossCardTests();
	validationTests();
	console.log('LuCI behavior tests passed');
})().catch((error) => { console.error(error); process.exit(1); });
