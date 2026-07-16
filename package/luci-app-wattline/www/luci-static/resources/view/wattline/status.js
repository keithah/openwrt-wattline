'use strict';
'require view';
'require uci';
'require poll';

/* Wattline status view — styled after the PeakDo Link-Power web app, with
   user-facing copy from the LinkPower-2 manual (runtime, Bypass, USB-C
   charge-only, Starlink reserve). */

var GREEN = '#25b45f', ORANGE = '#f5a623', GREY = '#9aa0a6', RED = '#e5533c';

function api(token, port, method, path, body) {
	var base = window.location.protocol + '//' + window.location.hostname + ':' + port + '/api/v1';
	return fetch(base + path, {
		method: method,
		headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' },
		body: body ? JSON.stringify(body) : null
	}).then(function (r) { return r.json(); });
}

function css() {
	return '' +
'.wl-wrap{max-width:460px;margin:0 auto;font-family:-apple-system,Segoe UI,Roboto,sans-serif;color:#202124}' +
'.wl-head{display:flex;align-items:center;justify-content:space-between;padding:6px 4px 12px}' +
'.wl-title{font-size:18px;font-weight:600}.wl-dev{color:#9aa0a6;font-size:12px}' +
'.wl-pill{font-size:11px;padding:2px 9px;border-radius:10px;background:#eef1f4;color:#5f6368;margin-left:8px;vertical-align:middle}' +
'.wl-pill.on{background:#e6f6ec;color:#25b45f}' +
'.wl-card{background:#fff;border-radius:16px;padding:18px;margin:12px 0;box-shadow:0 1px 4px rgba(0,0,0,.08)}' +
'.wl-ring{position:relative;width:170px;height:170px;margin:0 auto}' +
'.wl-ring svg{transform:rotate(-90deg)}' +
'.wl-pct{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center}' +
'.wl-pct b{font-size:46px;font-weight:600;line-height:1}.wl-pct span{font-size:15px;color:#9aa0a6}' +
'.wl-runtime{font-size:18px;font-weight:600;text-align:center;margin-top:8px}' +
'.wl-cardhead{display:flex;align-items:center;justify-content:space-between;margin-bottom:2px}' +
'.wl-cardhead .t{font-size:15px;font-weight:600;color:#3c4043}' +
'.wl-big{font-size:30px;font-weight:600;margin:6px 0 0}' +
'.wl-big .u{font-size:16px;color:#9aa0a6;font-weight:400;margin-left:3px}' +
'.wl-sub{color:#9aa0a6;font-size:13px}' +
'.wl-metrics{display:flex;margin-top:10px;align-items:flex-end}' +
'.wl-metric{margin-right:24px}.wl-metric b{font-size:18px}.wl-metric span{display:block;color:#9aa0a6;font-size:12px}' +
'.wl-sw{position:relative;width:46px;height:26px;border-radius:13px;background:#d0d4d9;cursor:pointer;flex:none}' +
'.wl-sw.on{background:#25b45f}.wl-sw::after{content:"";position:absolute;top:3px;left:3px;width:20px;height:20px;border-radius:50%;background:#fff;transition:left .15s;box-shadow:0 1px 2px rgba(0,0,0,.3)}' +
'.wl-sw.on::after{left:23px}' +
'.wl-note{color:#9aa0a6;font-size:12px;margin:6px 2px 0;line-height:1.5}' +
'.wl-msg{text-align:center;color:#9aa0a6;padding:26px 10px}';
}

function hm(min) {
	if (min == null || min <= 0) return null;
	var h = Math.floor(min / 60), m = min % 60;
	return (h > 0 ? h + ' h ' : '') + m + ' m';
}
function flow(s) { return s === -1 ? ORANGE : GREEN; }
function word(s) { return s === 1 ? 'Charging' : s === -1 ? 'Discharging' : 'Idle'; }

return view.extend({
	load: function () { return uci.load('wattline'); },
	render: function () {
		var token = uci.get('wattline', 'main', 'token') || '';
		var port = uci.get('wattline', 'main', 'port') || '8377';

		var conn = E('span', { 'class': 'wl-pill' }, _('…'));
		var dev = E('div', { 'class': 'wl-dev' }, 'Link-Power');
		var body = E('div', {}, E('div', { 'class': 'wl-msg' }, _('Loading…')));
		var wrap = E('div', { 'class': 'wl-wrap' }, [
			E('style', {}, css()),
			E('div', { 'class': 'wl-head' }, [E('div', {}, [E('div', { 'class': 'wl-title' }, 'Wattline'), dev]), conn]),
			body
		]);

		function act(action) { api(token, port, 'POST', '/device/action', { action: action }).catch(function () {}); }
		function sw(on, onA, offA) {
			var e = E('div', { 'class': 'wl-sw' + (on ? ' on' : '') });
			e.addEventListener('click', function () { act(on ? offA : onA); });
			return e;
		}
		function metric(v, l) { return E('div', { 'class': 'wl-metric' }, [E('b', {}, v), E('span', {}, l)]); }

		function refresh() {
			api(token, port, 'GET', '/telemetry').then(function (t) {
				body.innerHTML = '';
				if (!t || !t.connected) {
					conn.className = 'wl-pill'; conn.textContent = _('Disconnected');
					body.appendChild(E('div', { 'class': 'wl-card' }, E('div', { 'class': 'wl-msg' }, [
						E('div', { style: 'font-size:15px;color:#3c4043;margin-bottom:6px' }, _('No power bank connected')),
						_('Plug the USB BLE dongle into the router and power on the Link-Power. It will connect automatically.')
					])));
					return;
				}
				conn.className = 'wl-pill on'; conn.textContent = _('Connected');
				api(token, port, 'GET', '/status').then(function (s) {
					if (s && s.device) dev.textContent = 'Link-Power' + (s.device.model ? ' · ' + s.device.model : '') + (s.device.firmware ? ' · fw ' + s.device.firmware : '');
				}).catch(function () {});

				var b = t.battery || {}, dc = t.dc || {}, c = t.typec || {};
				var bColor = flow(b.status);
				var r = 74, circ = 2 * Math.PI * r, off = circ * (1 - (b.level || 0) / 100);
				var ringSvg = E('div', { 'class': 'wl-ring' }, [
					E('svg', { width: 170, height: 170, viewBox: '0 0 170 170' }, [
						E('circle', { cx: 85, cy: 85, r: r, fill: 'none', stroke: '#eef1f4', 'stroke-width': 12 }),
						E('circle', { cx: 85, cy: 85, r: r, fill: 'none', stroke: bColor, 'stroke-width': 12,
							'stroke-linecap': 'round', 'stroke-dasharray': circ.toFixed(1), 'stroke-dashoffset': off.toFixed(1) })
					]),
					E('div', { 'class': 'wl-pct' }, [E('b', { style: 'color:' + bColor }, String(b.level != null ? b.level : 0)), E('span', {}, '%')])
				]);
				var rt = hm(b.remain_min);
				var runtimeTxt = b.full ? _('Fully charged') : (rt ? (rt + (b.status === 1 ? _(' to full') : _(' remaining'))) : word(b.status));

				body.appendChild(E('div', { 'class': 'wl-card' }, [
					ringSvg,
					E('div', { 'class': 'wl-runtime', style: 'color:' + bColor }, runtimeTxt),
					E('div', { 'class': 'wl-sub', style: 'text-align:center' },
						word(b.status) + ' · ' + (b.wh != null ? b.wh.toFixed(1) : '—') + ' / ' + (b.max_wh != null ? b.max_wh.toFixed(0) : '—') + ' Wh'
						+ (b.volts != null ? ' · ' + b.volts.toFixed(1) + ' V' : ''))
				]));

				body.appendChild(E('div', { 'class': 'wl-card' }, [
					E('div', { 'class': 'wl-cardhead' }, [
						E('div', { 'class': 't' }, [_('DC Port'), dc.bypass ? E('span', { 'class': 'wl-pill on' }, _('Bypass on')) : '']),
						sw(!!dc.enabled, 'dc_on', 'dc_off')]),
					E('div', { 'class': 'wl-sub' }, _('Powers your Starlink Mini')),
					E('div', { 'class': 'wl-big', style: 'color:' + flow(dc.status) }, [(dc.watts != null ? dc.watts.toFixed(1) : '—'), E('span', { 'class': 'u' }, 'W')]),
					E('div', { 'class': 'wl-sub' }, dc.status === 1 ? _('Charging power') : _('Output power')),
					E('div', { 'class': 'wl-metrics' }, [
						metric((dc.volts != null ? dc.volts.toFixed(1) : '—') + ' V', _('Voltage')),
						metric((dc.amps != null ? dc.amps.toFixed(2) : '—') + ' A', _('Current')),
						E('div', { style: 'flex:1' }, ''),
						E('div', { style: 'text-align:right' }, [E('div', { 'class': 'wl-sub' }, _('Bypass')),
							E('div', { style: 'margin-top:3px;display:inline-block' }, sw(!!dc.bypass, 'bypass_on', 'bypass_off'))])
					])
				]));

				var cMode = c.mode || 0, cOut = (cMode === 2 || cMode === 3);
				var modeTxt = cMode === 3 ? _('Charge & Discharge') : cMode === 1 ? _('Charging only') : cMode === 2 ? _('Output only') : _('Off');
				var tempHigh = c.temp_c != null && c.temp_c >= 55;
				body.appendChild(E('div', { 'class': 'wl-card' }, [
					E('div', { 'class': 'wl-cardhead' }, [E('div', { 'class': 't' }, [_('USB-C Port'), E('span', { 'class': 'wl-pill' }, modeTxt)]), sw(cOut, 'usbc_on', 'usbc_off')]),
					E('div', { 'class': 'wl-big', style: 'color:' + flow(c.status) }, [(c.watts != null ? c.watts.toFixed(1) : '—'), E('span', { 'class': 'u' }, 'W')]),
					E('div', { 'class': 'wl-sub' }, c.status === 1 ? _('Charging power') : c.status === -1 ? _('Output power') : _('Idle')),
					E('div', { 'class': 'wl-metrics' }, [
						metric((c.volts != null ? c.volts.toFixed(1) : '—') + ' V', _('Voltage')),
						metric((c.amps != null ? c.amps.toFixed(2) : '—') + ' A', _('Current')),
						metric(E('span', { style: tempHigh ? 'color:' + RED : '' }, (c.temp_c != null ? c.temp_c.toFixed(0) : '—') + ' °C'), _('Temp'))
					])
				]));

				body.appendChild(E('div', { 'class': 'wl-note' },
					_('~10–15% of the battery is reserved for the Starlink Mini — USB-C output turns off automatically below that to keep your dish running.')));
			}).catch(function () {
				body.innerHTML = ''; conn.className = 'wl-pill'; conn.textContent = _('Offline');
				body.appendChild(E('div', { 'class': 'wl-card' }, E('div', { 'class': 'wl-msg' }, _('Daemon unreachable — is wattlined running? (/etc/init.d/wattlined start)'))));
			});
		}
		refresh();
		poll.add(refresh, 2);
		return wrap;
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});
