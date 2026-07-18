'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const root = path.resolve(__dirname, '..');
const fixture = require('./power_loss_preset.json');

function loadModule(name) {
	const source = fs.readFileSync(path.join(root,
		'luci-app-wattline/www/luci-static/resources/wattline', name), 'utf8');
	return Function(source)();
}

const powerLoss = loadModule('power_loss.js');

assert.deepStrictEqual(powerLoss.payload(null, true, 10, false), fixture.canonical);
assert.strictEqual(powerLoss.classify([fixture.canonical]).kind, 'compatible');
assert.strictEqual(powerLoss.classify([{ name: fixture.name, condition: 'schedule' }]).kind, 'conflict');
assert.throws(() => powerLoss.payload(null, true, 0, false), /1.*1440/);
assert.throws(() => powerLoss.payload(null, true, 1441, false), /1.*1440/);
assert.throws(() => powerLoss.payload(null, true, 1.5, false), /whole number/);

assert.strictEqual(powerLoss.classify([]).kind, 'missing');
assert.strictEqual(powerLoss.classify([Object.assign({}, fixture.canonical, { state: 'present' })]).kind,
	'conflict');
assert.strictEqual(powerLoss.classify([Object.assign({}, fixture.canonical, { actions: ['dc_off'] })]).kind,
	'conflict');
assert.strictEqual(powerLoss.minutes(fixture.canonical), 10);

const customized = Object.assign({}, fixture.canonical, {
	actions: ['webhook:https://example.test/input-lost', 'shutdown'],
	repeat_every: 1800000000000
});
const preserved = powerLoss.payload(customized, false, 17, false);
assert.deepStrictEqual(preserved.actions, customized.actions);
assert.strictEqual(preserved.repeat_every, 1800000000000);
assert.strictEqual(preserved.enabled, false);
assert.strictEqual(preserved.hold, 1020000000000);
assert.notStrictEqual(preserved, customized);

const incompatible = { name: fixture.name, condition: 'schedule', actions: ['shutdown'], cron: '0 1 * * *' };
assert.throws(() => powerLoss.payload(incompatible, true, 10, false), /conflict/i);
assert.deepStrictEqual(powerLoss.payload(incompatible, false, 12, true), Object.assign({}, fixture.canonical, {
	enabled: false,
	hold: 720000000000
}));

const telemetry = {
	connected: true,
	battery: { status: 0 },
	typec: { dc_input: false }
};
assert.strictEqual(powerLoss.display(fixture.canonical, { rules: [] },
	Object.assign({}, telemetry, { connected: false })).kind, 'disconnected');
assert.strictEqual(powerLoss.display(fixture.canonical, { rules: [] },
	Object.assign({}, telemetry, { battery: { status: 1 } })).kind, 'present');
assert.strictEqual(powerLoss.display(fixture.canonical, { rules: [] },
	Object.assign({}, telemetry, { typec: { dc_input: true } })).kind, 'present');

const holding = powerLoss.display(fixture.canonical, { rules: [{
	name: 'unrelated_rule',
	armed: false,
	last_fired: '2026-07-17T11:00:00Z'
}, {
	name: fixture.name,
	armed: true,
	holding_for: '5m0s'
}] }, telemetry);
assert.strictEqual(holding.kind, 'holding');
assert.strictEqual(holding.remainingSeconds, 300);

const longHolding = powerLoss.display(Object.assign({}, fixture.canonical, {
	hold: 3783000000000
}), { rules: [{
	name: fixture.name,
	armed: true,
	holding_for: '1h2m3s'
}] }, telemetry);
assert.strictEqual(longHolding.remainingSeconds, 60);

const justStarted = powerLoss.display(fixture.canonical, { rules: [{
	name: fixture.name,
	armed: true,
	holding_for: '0s'
}] }, telemetry);
assert.strictEqual(justStarted.remainingSeconds, 600);

const overdue = powerLoss.display(fixture.canonical, { rules: [{
	name: fixture.name,
	armed: true,
	holding_for: '1h2m3s'
}] }, telemetry);
assert.strictEqual(overdue.remainingSeconds, 0, 'remaining time is never negative');

const fired = powerLoss.display(fixture.canonical, { rules: [{
	name: fixture.name,
	armed: false,
	holding_for: '10m0s',
	last_fired: '2026-07-17T12:00:00Z'
}] }, telemetry);
assert.strictEqual(fired.kind, 'fired');
assert.strictEqual(fired.lastFired, '2026-07-17T12:00:00Z');
assert.strictEqual(Object.prototype.hasOwnProperty.call(fired, 'succeeded'), false,
	'last_fired is not proof that shutdown succeeded');

const rearmedHolding = powerLoss.display(fixture.canonical, { rules: [{
	name: fixture.name,
	armed: true,
	last_fired: '2026-07-17T12:00:00Z'
}] }, telemetry);
assert.strictEqual(rearmedHolding.kind, 'holding',
	'an armed rule with a historical firing is holding during a new input loss');
assert.strictEqual(rearmedHolding.remainingSeconds, 600);

console.log('Power-loss preset behavior tests passed');
