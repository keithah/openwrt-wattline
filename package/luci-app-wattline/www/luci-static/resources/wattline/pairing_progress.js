'use strict';

function phases(status) {
	var events = Array.isArray(status && status.events) ? status.events : [];
	var recovering = status && status.phase === 'clearing_stale_bond' || events.some(function (event) {
		return event && event.phase === 'clearing_stale_bond';
	});
	var result = [
		{ phase: 'preparing_adapter', label: 'Preparing adapter' },
		{ phase: 'locating_device', label: 'Locating device' },
		{ phase: 'exchanging_pin', label: 'Exchanging PIN' },
		{ phase: 'confirming_bond', label: 'Confirming bond' },
		{ phase: 'trusting_device', label: 'Trusting device' },
		{ phase: 'reconnecting', label: 'Reconnecting' },
		{ phase: 'verifying_handshake', label: 'Verifying handshake' },
		{ phase: 'complete', label: 'Saved' }
	];
	if (recovering) result.splice(1, 0, { phase: 'clearing_stale_bond', label: 'Clearing stale router bond' });
	return result;
}

function model(status) {
	status = status || {};
	var events = Array.isArray(status.events) ? status.events.map(function (event) {
		return { at: event.at || '', phase: event.phase || '', message: event.message || '' };
	}) : [];
	var steps = phases(status), current = status.phase || '';
	if (current === 'saving_pairing') current = 'complete';
	if (current === 'failed') {
		for (var i = events.length - 1; i >= 0; i--) {
			if (events[i].phase && events[i].phase !== 'failed') {
				current = events[i].phase === 'saving_pairing' ? 'complete' : events[i].phase;
				break;
			}
		}
	}
	var currentIndex = steps.map(function (step) { return step.phase; }).indexOf(current);
	var terminal = status.stage === 'paired' && status.phase === 'complete';
	var failed = status.stage === 'error';
	steps = steps.map(function (step, index) {
		var state = 'pending';
		if (terminal || index < currentIndex) state = 'complete';
		else if (index === currentIndex) state = failed ? 'failed' : 'active';
		return { phase: step.phase, label: step.label, state: state };
	});
	var elapsedSeconds = Math.max(0, Math.floor(Number(status.elapsed_ms) / 1000) || 0);
	var elapsed = elapsedSeconds >= 60 ? Math.floor(elapsedSeconds / 60) + 'm ' + (elapsedSeconds % 60) + 's' : elapsedSeconds + 's';
	return { steps: steps, message: status.message || status.error || '', elapsed: elapsed, events: events };
}

return { model: model };
