'use strict';

var presetName = 'no_input_shutdown';
var nanosecondsPerMinute = 60000000000;

function canonical() {
	return {
		name: presetName,
		enabled: true,
		condition: 'input_power',
		state: 'absent',
		hold: 10 * nanosecondsPerMinute,
		hysteresis_margin: 5,
		actions: ['shutdown'],
		confirm_shutdown: true
	};
}

function compatible(rule) {
	return !!rule && rule.name === presetName &&
		rule.condition === 'input_power' && rule.state === 'absent' &&
		Array.isArray(rule.actions) && rule.actions.indexOf('shutdown') !== -1;
}

function classify(rules) {
	var list = Array.isArray(rules) ? rules : [];
	var rule = null;
	for (var i = 0; i < list.length; i++) {
		if (list[i] && list[i].name === presetName) {
			rule = list[i];
			break;
		}
	}
	if (!rule) return { kind: 'missing', rule: null };
	return { kind: compatible(rule) ? 'compatible' : 'conflict', rule: rule };
}

function payload(existing, enabled, minutes, reset) {
	var delay = Number(minutes);
	if (!Number.isInteger(delay) || delay < 1 || delay > 1440)
		throw new Error('Delay must be a whole number from 1 to 1440 minutes');
	if (existing && !reset && !compatible(existing))
		throw new Error('Customized rule conflict requires resetting the preset');
	var rule = reset || !existing ? canonical() : Object.assign({}, existing);
	rule.name = 'no_input_shutdown';
	rule.enabled = !!enabled;
	rule.hold = delay * 60000000000;
	rule.confirm_shutdown = true;
	return rule;
}

function minutes(rule) {
	var value = Number(rule && rule.hold) / nanosecondsPerMinute;
	if (!Number.isFinite(value) || value <= 0) return 10;
	return Math.max(1, Math.min(1440, Math.round(value)));
}

function durationSeconds(value) {
	var match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(String(value || ''));
	if (!match || (!match[1] && !match[2] && !match[3])) return 0;
	return Number(match[1] || 0) * 3600 + Number(match[2] || 0) * 60 + Number(match[3] || 0);
}

function matchingStatus(status) {
	var rules = Array.isArray(status) ? status : status && status.rules;
	if (!Array.isArray(rules)) return null;
	for (var i = 0; i < rules.length; i++) {
		if (rules[i] && rules[i].name === presetName) return rules[i];
	}
	return null;
}

function display(rule, status, telemetry) {
	if (!telemetry || telemetry.connected !== true)
		return { kind: 'disconnected', remainingSeconds: null };

	var inputPresent = !!(
		(telemetry.battery && telemetry.battery.status === 1) ||
		(telemetry.typec && telemetry.typec.dc_input === true)
	);
	if (inputPresent) return { kind: 'present', remainingSeconds: null };

	var runtime = matchingStatus(status);
	if (runtime && runtime.armed === false && runtime.last_fired) {
		return { kind: 'fired', remainingSeconds: 0, lastFired: runtime.last_fired };
	}

	if (runtime && runtime.holding_for) {
		var total = Math.max(0, Number(rule && rule.hold) / 1000000000);
		return {
			kind: 'holding',
			remainingSeconds: Math.max(0, Math.round(total - durationSeconds(runtime.holding_for)))
		};
	}

	return {
		kind: 'holding',
		remainingSeconds: Math.max(0, Math.round(Number(rule && rule.hold) / 1000000000) || 0)
	};
}

return { classify: classify, payload: payload, minutes: minutes, display: display };
