'use strict';
'require view';
'require uci';
'require poll';
'require wattline.transport as wattlineTransport';
'require wattline.qr as wattlineQR';
'require wattline.refresh as wattlineRefresh';

/* Wattline status view — styled after the PeakDo Link-Power web app, with
   user-facing copy from the LinkPower-2 manual (runtime, Bypass, USB-C
   charge-only, Starlink reserve). */

var GREEN = '#25b45f', ORANGE = '#f5a623', GREY = '#9aa0a6', RED = '#e5533c';

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
'.wl-msg{text-align:center;color:#9aa0a6;padding:26px 10px}' +
'.wl-btn{padding:8px 18px;border-radius:8px;font-size:14px;cursor:pointer;border:1px solid #d0d4d9;background:#fff;color:#3c4043}' +
'.wl-btn.primary{border:none;background:#25b45f;color:#fff}' +
'.wl-btn.danger{border-color:#c94332;color:#b53225}' +
'.wl-btn[disabled]{opacity:.5;cursor:default}' +
'.wl-devrow{display:flex;justify-content:space-between;align-items:center;padding:10px 12px;border-radius:10px;cursor:pointer;margin-top:6px;border:1px solid #e4e7eb}' +
'.wl-devrow.sel{border:2px solid #25b45f;padding:9px 11px}' +
'.wl-pin{width:90px;padding:7px 9px;font-size:14px;border:1px solid #d0d4d9;border-radius:8px;margin-top:2px}' +
'.wl-grid{display:grid;grid-template-columns:minmax(120px,1fr) minmax(0,2fr);gap:7px 14px;margin-top:10px}' +
'.wl-key{color:#717780;font-size:12px}.wl-value{font-size:12px;overflow-wrap:anywhere}' +
'.wl-token{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;align-items:center;padding:10px 0;border-top:1px solid #e8eaed}' +
'.wl-qr{display:block;width:180px;height:180px;margin:12px auto;border:1px solid #d7dadd;border-radius:8px}' +
'.wl-actions{display:flex;flex-wrap:wrap;gap:8px;margin-top:12px}' +
'@media(max-width:420px){.wl-grid{grid-template-columns:1fr}.wl-key{margin-top:5px}}';
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
		var port = {
			httpsEnabled: uci.get('wattline', 'main', 'https_enabled') !== '0',
			httpsPort: uci.get('wattline', 'main', 'https_port') || '8378',
			httpEnabled: uci.get('wattline', 'main', 'http_enabled') !== '0',
			httpPort: uci.get('wattline', 'main', 'port') || '8377'
		};
		var client = wattlineTransport.create({ token: token, config: port, host: window.location.hostname, fetch: fetch });
		function api(unusedToken, unusedConfig, method, path, body) { return client.json(method, path, body); }

		var conn = E('span', { 'class': 'wl-pill' }, _('…'));
		var dev = E('div', { 'class': 'wl-dev' }, 'Link-Power');
		var body = E('div', {}, E('div', { 'class': 'wl-msg' }, _('Loading…')));
		// `extra` holds device settings/schedules/power. It is rebuilt only on
		// connect and after a control action — NOT on every 2s telemetry poll —
		// so its text inputs keep focus while the user types.
		var extra = E('div', {});
		var admin = E('div', {}, E('div', { 'class': 'wl-card' }, E('div', { 'class': 'wl-msg' }, _('Loading device administration…'))));
		var wrap = E('div', { 'class': 'wl-wrap' }, [
			E('style', {}, css()),
			E('div', { 'class': 'wl-head' }, [E('div', {}, [E('div', { 'class': 'wl-title' }, 'Wattline'), dev]), conn]),
			body, extra, admin
		]);

		function act(action) { api(token, port, 'POST', '/device/action', { action: action }).catch(function () {}); }

		/* Device settings/schedules state (persists across telemetry polls). */
		var usbcLimit = null, threshold = null, thrInput = '', schedules = null;
		var ctlErr = '', extraLoaded = false, newSch = { type: 1, hour: 8, minute: 0, action: 1 };

		function fetchExtras(rebuild) {
			Promise.all([
				api(token, port, 'GET', '/device/usbc-limit').then(function (l) { usbcLimit = l; }).catch(function () {}),
				api(token, port, 'GET', '/device/bypass-threshold').then(function (t) {
					threshold = (t && typeof t.volts === 'number') ? t.volts : null;
					if (threshold != null && thrInput === '') thrInput = String(threshold);
				}).catch(function () {}),
				api(token, port, 'GET', '/device/schedules').then(function (s) { schedules = Array.isArray(s) ? s : []; }).catch(function () {})
			]).then(function () { if (rebuild) buildExtra(); });
		}
		function ctlPost(path, bodyObj) {
			ctlErr = '';
			return api(token, port, 'POST', path, bodyObj)
				.then(function () { fetchExtras(true); })
				.catch(function (e) { ctlErr = e.message; buildExtra(); });
		}
		function buildExtra() {
			extra.innerHTML = '';
			if (!extraLoaded) return;
			var wattChoices = [30, 45, 60, 65, 100, 140];
			var curWatts = usbcLimit && usbcLimit.output ? usbcLimit.output.watts : 0;
			var limitBtns = wattChoices.map(function (wv) {
				var b = E('button', { 'class': 'wl-btn' + (curWatts === wv ? ' primary' : ''), style: 'margin:0 6px 6px 0' }, wv + ' W');
				b.addEventListener('click', function () { ctlPost('/device/usbc-limit', { type: 'output', watts: wv }); });
				return b;
			});
			var thrIn = E('input', { 'class': 'wl-pin', style: 'width:80px', inputmode: 'decimal', value: thrInput });
			thrIn.addEventListener('input', function () { thrInput = thrIn.value; });
			var thrBtn = E('button', { 'class': 'wl-btn' }, _('Set'));
			thrBtn.addEventListener('click', function () {
				var v = parseFloat(thrInput);
				if (!(v > 0)) { ctlErr = _('Enter a voltage'); buildExtra(); return; }
				ctlPost('/device/bypass-threshold', { volts: v });
			});
			var settings = E('div', { 'class': 'wl-card' }, [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Device settings'))),
				E('div', { 'class': 'wl-sub' }, _('USB-C output power limit')),
				E('div', { style: 'display:flex;flex-wrap:wrap;margin-top:6px' }, limitBtns),
				E('div', { style: 'display:flex;align-items:flex-end;margin-top:12px' }, [
					E('div', { style: 'margin-right:10px' }, [E('div', { 'class': 'wl-sub' }, _('DC bypass engages at (V)')), thrIn]),
					E('div', { style: 'flex:1' }, ''), thrBtn
				]),
				ctlErr ? E('div', { style: 'color:' + RED + ';font-size:12px;margin-top:8px' }, ctlErr) : E('span', {})
			]);

			var dayType = function (ty) { return ty === 0 ? _('Once') : ty === 1 ? _('Daily') : ty === 2 ? _('Weekly') : _('Monthly'); };
			var hhmm = function (t) { return ('0' + t.hour).slice(-2) + ':' + ('0' + t.minute).slice(-2); };
			var schKids = [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Schedules'))),
				E('div', { 'class': 'wl-sub' }, _('On/off timers stored on the device — they run even if the router is offline.'))
			];
			(schedules || []).forEach(function (t) {
				var del = E('button', { 'class': 'wl-btn' }, _('Delete'));
				del.addEventListener('click', function () {
					ctlErr = '';
					api(token, port, 'DELETE', '/device/schedules/' + t.id).then(function () { fetchExtras(true); })
						.catch(function (e) { ctlErr = e.message; buildExtra(); });
				});
				schKids.push(E('div', { class: 'wl-devrow', style: 'cursor:default' }, [
					E('div', {}, [E('b', { style: 'font-size:14px' }, hhmm(t) + ' · ' + (t.action ? _('On') : _('Off'))),
						E('div', { 'class': 'wl-sub', style: 'font-size:12px' }, dayType(t.type) + (t.status === 1 ? '' : ' · ' + _('disabled')))]),
					del
				]));
			});
			if (!(schedules || []).length) schKids.push(E('div', { 'class': 'wl-sub', style: 'margin-top:6px' }, _('No schedules yet.')));
			var mk = function (val, opts, onch) {
				var sel = E('select', { style: 'padding:6px 8px;border-radius:8px;margin-right:6px;border:1px solid #d0d4d9' },
					opts.map(function (o) { return E('option', { value: o[0] }, o[1]); }));
				sel.value = String(val); sel.addEventListener('change', function () { onch(sel.value); });
				return sel;
			};
			var hrIn = E('input', { 'class': 'wl-pin', style: 'width:48px', inputmode: 'numeric', value: newSch.hour });
			hrIn.addEventListener('input', function () { newSch.hour = hrIn.value; });
			var minIn = E('input', { 'class': 'wl-pin', style: 'width:48px', inputmode: 'numeric', value: newSch.minute });
			minIn.addEventListener('input', function () { newSch.minute = minIn.value; });
			var addBtn = E('button', { 'class': 'wl-btn primary' }, _('Add'));
			addBtn.addEventListener('click', function () {
				ctlPost('/device/schedules', { type: Number(newSch.type), hour: Number(newSch.hour), minute: Number(newSch.minute), action: Number(newSch.action) });
			});
			schKids.push(E('div', { style: 'display:flex;flex-wrap:wrap;align-items:center;margin-top:10px' }, [
				mk(newSch.type, [[1, _('Daily')], [0, _('Once')], [2, _('Weekly')], [3, _('Monthly')]], function (v) { newSch.type = v; }),
				hrIn, E('span', { 'class': 'wl-sub' }, ':'), minIn,
				mk(newSch.action, [[1, _('Turn on')], [0, _('Turn off')]], function (v) { newSch.action = v; }),
				addBtn
			]));
			var schedCard = E('div', { 'class': 'wl-card' }, schKids);

			var restart = E('button', { 'class': 'wl-btn' }, _('Restart'));
			restart.addEventListener('click', function () {
				if (window.confirm(_('Restart the Link-Power? It will reconnect in about 15 seconds.'))) act('restart');
			});
			var poweroff = E('button', { 'class': 'wl-btn', style: 'border:none;background:' + RED + ';color:#fff' }, _('Power off'));
			poweroff.addEventListener('click', function () {
				if (window.confirm(_('Power OFF the Link-Power completely? This is a hard shutdown — the device (and anything it powers) turns off and will NOT return over Bluetooth until physically powered on again.'))) act('shutdown');
			});
			var powerCard = E('div', { 'class': 'wl-card' }, [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Power'))),
				E('div', { style: 'display:flex;gap:8px;margin-top:8px' }, [restart, poweroff])
			]);

			extra.appendChild(settings);
			extra.appendChild(schedCard);
			extra.appendChild(powerCard);
		}

		/* Pairing state persists across polls; the card is only rebuilt when
		   the pairing status changes so the PIN input survives refreshes.
		   lastP caches the last /pairing/status so selection changes redraw
		   locally, pollN gates the idle polling rate, and gen discards stale
		   async responses that would otherwise append onto a rebuilt body. */
		var selMac = '', pin = '020555', pairCard = null, pairKey = null;
		var lastP = null, pairMsg = '', pollN = 0, gen = 0;
		function redrawPairCard() {
			var old = pairCard;
			pairKey = null;
			pairCard = buildPairCard(lastP || { stage: 'idle', devices: [] });
			if (old && old.parentNode) old.parentNode.replaceChild(pairCard, old);
		}
		function buildPairCard(p) {
			var busy = p.stage === 'scanning' || p.stage === 'pairing';
			var kids = [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Pair Link-Power over BLE'))),
				E('div', { 'class': 'wl-sub' }, _('Power on the Link-Power, keep it near the router, then scan. Make sure no phone or laptop app is connected to it.'))
			];
			var scanBtn = E('button', { 'class': 'wl-btn', style: 'margin-top:12px' },
				p.stage === 'scanning' ? _('Scanning…') : _('Scan for devices'));
			if (busy) scanBtn.setAttribute('disabled', '');
			scanBtn.addEventListener('click', function () {
				pairMsg = '';
				api(token, port, 'POST', '/pairing/scan')
					.then(function () { pollN = 0; pairKey = null; refresh(); })
					.catch(function (e) { pairMsg = e.message; redrawPairCard(); });
			});
			kids.push(scanBtn);
			(p.devices || []).forEach(function (d) {
				var row = E('div', { 'class': 'wl-devrow' + (selMac === d.mac ? ' sel' : '') }, [
					E('div', {}, [E('b', { style: 'font-size:14px' }, d.name || _('(unnamed)')),
						E('div', { 'class': 'wl-sub', style: 'font-size:12px' }, d.mac + (d.paired ? _(' · previously paired') : ''))]),
					E('div', { 'class': 'wl-sub', style: 'font-size:12px' }, d.rssi ? d.rssi + ' dBm' : '')
				]);
				row.addEventListener('click', function () { selMac = d.mac; redrawPairCard(); });
				kids.push(row);
			});
			if (selMac) {
				var pinInput = E('input', { 'class': 'wl-pin', maxlength: 6, value: pin, inputmode: 'numeric' });
				pinInput.addEventListener('input', function () {
					pinInput.value = pinInput.value.replace(/[^0-9]/g, '');
					pin = pinInput.value;
				});
				var pairBtn = E('button', { 'class': 'wl-btn primary' }, p.stage === 'pairing' ? _('Pairing…') : _('Pair'));
				if (busy) pairBtn.setAttribute('disabled', '');
				pairBtn.addEventListener('click', function () {
					pairMsg = '';
					api(token, port, 'POST', '/pairing/pair', { mac: selMac, pin: pin })
						.then(function () { pollN = 0; pairKey = null; refresh(); })
						.catch(function (e) { pairMsg = e.message; redrawPairCard(); });
				});
				kids.push(E('div', { style: 'display:flex;align-items:flex-end;margin-top:12px' }, [
					E('div', { style: 'margin-right:10px' }, [E('div', { 'class': 'wl-sub' }, _('PIN')), pinInput]),
					E('div', { style: 'flex:1' }, ''), pairBtn
				]));
				kids.push(E('div', { 'class': 'wl-note' },
					_('Default PIN is 020555 (see the manual). If the device shows a PIN on its screen, enter that instead.')));
			}
			if (pairMsg) kids.push(E('div', { style: 'color:' + RED + ';font-size:13px;margin-top:10px' }, pairMsg));
			if (p.stage === 'pairing') kids.push(E('div', { style: 'color:' + ORANGE + ';font-size:13px;margin-top:10px' }, _('Pairing and verifying the connection… this usually takes under a minute.')));
			if (p.stage === 'paired') kids.push(E('div', { style: 'color:' + GREEN + ';font-size:13px;margin-top:10px' }, _('Paired. Connecting…')));
			if (p.stage === 'error') kids.push(E('div', { style: 'color:' + RED + ';font-size:13px;margin-top:10px' }, _('Pairing failed: ') + (p.error || _('unknown error'))));
			return E('div', { 'class': 'wl-card' }, kids);
		}
		function sw(on, onA, offA) {
			var e = E('div', { 'class': 'wl-sw' + (on ? ' on' : '') });
			e.addEventListener('click', function () { act(on ? offA : onA); });
			return e;
		}
		function metric(v, l) { return E('div', { 'class': 'wl-metric' }, [E('b', {}, v), E('span', {}, l)]); }

		/* Router/API administration is intentionally separate from BLE-device
		   pairing. The bootstrap token is sent only in the Authorization header;
		   enrollment images are short-lived object URLs and never data/query URIs. */
		var qrImage = null, pairingExpiresAt = null;
		var countdown = E('span', { 'class': 'wl-pill' }, _('Closed'));
		var qr = wattlineQR.create({
			fetchBlob: function (signal) { return client.blob('GET', '/pairing-mode/qr.png', null, { signal: signal }); },
			createObjectURL: function (blob) { return URL.createObjectURL(blob); },
			revokeObjectURL: function (value) { URL.revokeObjectURL(value); }
		});
		function clearQR() { qr.close(qrImage); qrImage = null; }
		window.addEventListener('pagehide', clearQR, { once: true });

		function formatDate(value) {
			if (!value) return _('Never');
			var date = new Date(value);
			return isNaN(date.getTime()) ? value : date.toLocaleString();
		}
		function rows(items) {
			var children = [];
			items.forEach(function (item) {
				children.push(E('div', { 'class': 'wl-key' }, item[0]));
				children.push(E('div', { 'class': 'wl-value' }, item[1] == null || item[1] === '' ? '—' : String(item[1])));
			});
			return E('div', { 'class': 'wl-grid' }, children);
		}
		var adminRefresh;
		function adminAction(method, path, payload, confirmText) {
			if (confirmText && !window.confirm(confirmText)) return Promise.resolve();
			return adminRefresh.mutation(function () { return api(token, port, method, path, payload); })
				.catch(function (error) { window.alert(error.message); });
		}
		function loadQR(image, expiresAt) {
			qrImage = image;
			qr.load(image, expiresAt).catch(function (error) {
				if (image.parentNode) image.replaceWith(E('div', { 'class': 'wl-note' }, error.message));
			});
		}
		function updateCountdown() {
			if (!pairingExpiresAt) { countdown.textContent = _('Closed'); countdown.className = 'wl-pill'; return; }
			var seconds = Math.max(0, Math.ceil((pairingExpiresAt.getTime() - Date.now()) / 1000));
			countdown.textContent = seconds > 0 ? Math.floor(seconds / 60) + ':' + ('0' + (seconds % 60)).slice(-2) : _('Expired');
			countdown.className = seconds > 0 ? 'wl-pill on' : 'wl-pill';
		}
		function renderAdmin(deviceInfo, settingsInfo, tokenInfo, pairInfo) {
			admin.innerHTML = '';
			var features = deviceInfo.features || {};
			var capabilityNames = Object.keys(features).filter(function (key) { return features[key]; })
				.map(function (key) { return key.replace(/_/g, ' '); }).join(', ') || _('None reported');
			var active = deviceInfo.commands && deviceInfo.commands.active || [];
			var identity = E('div', { 'class': 'wl-card' }, [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Device identity'))),
				rows([
					[_('Model'), deviceInfo.model], [_('Hardware / variant'), deviceInfo.hardware_revision],
					[_('Application firmware'), deviceInfo.application_firmware], [_('OTA bootloader'), deviceInfo.ota_firmware],
					[_('Device ID / MAC'), deviceInfo.id], [_('CID'), deviceInfo.cid],
					[_('Capabilities'), capabilityNames], [_('Connection state'), deviceInfo.connection && deviceInfo.connection.phase],
					[_('Pending commands'), active.length ? active.map(function (command) { return command.operation + ' · ' + command.phase; }).join(', ') : _('None')],
					[_('MagicDNS'), deviceInfo.magic_dns_name], [_('TLS certificate SHA-256'), settingsInfo.tls && settingsInfo.tls.sha256]
				])
			]);
			admin.appendChild(identity);

			var pairKids = [
				E('div', { 'class': 'wl-cardhead' }, [E('div', { 'class': 't' }, _('Pair an API client')), countdown]),
				E('div', { 'class': 'wl-sub' }, _('Open a short enrollment window, then scan this QR in a Wattline client. This is separate from Pair Link-Power over BLE.'))
			];
			if (settingsInfo.pairing_always_on) pairKids.push(E('div', { 'class': 'wl-note', style: 'color:' + RED },
				_('Pairing is always available to anyone with the PIN. Disable always-on pairing for a smaller attack window.')));
			pairingExpiresAt = pairInfo.open && pairInfo.expires_at ? new Date(pairInfo.expires_at) : null;
			if (pairInfo.open) {
				pairKids.push(E('div', { style: 'text-align:center;margin-top:10px' }, [
					E('div', { 'class': 'wl-sub' }, _('Enrollment PIN')),
					E('div', { style: 'font:600 30px ui-monospace,SFMono-Regular,monospace;letter-spacing:.14em' }, pairInfo.pin)
				]));
				var image = E('img', { 'class': 'wl-qr', alt: _('API-client enrollment QR code') });
				pairKids.push(image);
				var close = E('button', { 'class': 'wl-btn danger' }, _('Close pairing window'));
				close.addEventListener('click', function () {
					clearQR(); pairingExpiresAt = null; updateCountdown();
					adminAction('DELETE', '/pairing-mode', null);
				});
				pairKids.push(E('div', { 'class': 'wl-actions' }, close));
				loadQR(image, pairInfo.expires_at);
			} else {
				clearQR();
				var open = E('button', { 'class': 'wl-btn primary' }, _('Pair an API client'));
				open.addEventListener('click', function () { adminAction('POST', '/pairing-mode', null); });
				pairKids.push(E('div', { 'class': 'wl-actions' }, open));
			}
			admin.appendChild(E('div', { 'class': 'wl-card' }, pairKids));
			updateCountdown();

			var tokenKids = [E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('API clients'))),
				E('div', { 'class': 'wl-sub' }, _('Token secrets are shown only once to the client. This list contains metadata only.'))];
			(tokenInfo || []).forEach(function (entry) {
				var actions = E('span', {});
				if (!entry.bootstrap) {
					var revoke = E('button', { 'class': 'wl-btn danger' }, _('Revoke'));
					revoke.addEventListener('click', function () {
						adminAction('DELETE', '/tokens/' + encodeURIComponent(entry.id), null,
							_('Revoke this API client token immediately? The client will be disconnected.'));
					});
					actions.appendChild(revoke);
				} else actions.textContent = _('Administrator');
				tokenKids.push(E('div', { 'class': 'wl-token' }, [
					E('div', {}, [E('b', {}, entry.label), E('div', { 'class': 'wl-sub' },
						_('Created') + ' ' + formatDate(entry.created_at) + ' · ' + _('Last seen') + ' ' + formatDate(entry.last_seen_at))]), actions
				]));
			});
			admin.appendChild(E('div', { 'class': 'wl-card' }, tokenKids));

			var securityKids = [
				E('div', { 'class': 'wl-cardhead' }, E('div', { 'class': 't' }, _('Security and advanced actions'))),
				E('div', { 'class': 'wl-note' }, settingsInfo.wan_access ? _('WAN access is insecure — use TLS/VPN.') : _('WAN access is off. Remote access remains available through Tailscale or another VPN.'))
			];
			var rotate = E('button', { 'class': 'wl-btn' }, _('Rotate TLS certificate'));
			rotate.addEventListener('click', function () { adminAction('POST', '/tls/rotate', { confirm: true }, _('Rotate TLS certificate? Saved clients must accept and pin the new fingerprint after wattlined restarts.')); });
			var shutdown = E('button', { 'class': 'wl-btn danger' }, _('Shut down Link-Power'));
			shutdown.addEventListener('click', function () { adminAction('POST', '/device/shutdown', { confirm: true }, _('Shut down Link-Power? It will disarm reconnect and remain off until physically powered on.')); });
			securityKids.push(E('div', { 'class': 'wl-actions' }, [rotate, shutdown]));
			if (settingsInfo.advanced) {
				var ota = E('button', { 'class': 'wl-btn danger' }, _('Enter OTA mode'));
				ota.addEventListener('click', function () { adminAction('POST', '/device/ota/enter', { confirm: true }, _('Enter OTA mode? Wattline does not flash firmware; use this only for diagnostics.')); });
				var running = E('button', { 'class': 'wl-btn danger' }, _('Factory running mode'));
				running.addEventListener('click', function () {
					var mode = window.prompt(_('Factory running mode value (device enum):'), '1');
					if (mode != null && window.confirm(_('Set Factory running mode to ') + mode + '?')) adminAction('PUT', '/device/advanced/running-mode', { mode: Number(mode) });
				});
				var blePIN = E('button', { 'class': 'wl-btn danger' }, _('Set BLE PIN'));
				blePIN.addEventListener('click', function () {
					var nextPIN = window.prompt(_('New six-digit BLE PIN:'));
					if (nextPIN != null && window.confirm(_('Set BLE PIN? Existing BLE clients may need to pair again.'))) adminAction('PUT', '/device/advanced/ble-pin', { pin: nextPIN });
				});
				securityKids.push(E('div', { 'class': 'wl-actions' }, [ota, running, blePIN]));
			}
			admin.appendChild(E('div', { 'class': 'wl-card' }, securityKids));
		}
		adminRefresh = wattlineRefresh.create({
			load: function () {
				return Promise.all([
					api(token, port, 'GET', '/device'), api(token, port, 'GET', '/settings'),
					api(token, port, 'GET', '/tokens'), api(token, port, 'GET', '/pairing-mode')
				]);
			},
			render: function (values) { renderAdmin(values[0], values[1], values[2], values[3]); },
			error: function (error) {
					admin.innerHTML = '';
					admin.appendChild(E('div', { 'class': 'wl-card' }, E('div', { 'class': 'wl-msg' }, error.message)));
				}
		});
		function refreshAdmin() { return adminRefresh.refresh(); }

		function refresh() {
			var myGen = ++gen;
			api(token, port, 'GET', '/telemetry').then(function (t) {
				if (myGen !== gen) return; // superseded by a newer poll
				body.innerHTML = '';
				if (!t || !t.connected) {
					conn.className = 'wl-pill'; conn.textContent = _('Disconnected');
					if (extraLoaded) { extraLoaded = false; usbcLimit = null; schedules = null; extra.innerHTML = ''; }
					body.appendChild(E('div', { 'class': 'wl-card' }, E('div', { 'class': 'wl-msg' }, [
						E('div', { style: 'font-size:15px;color:#3c4043;margin-bottom:6px' }, _('No power bank connected')),
						_('Plug the USB BLE dongle into the router and power on the Link-Power. Already-paired devices connect automatically.')
					])));
					var g = gen;
					var busyStage = lastP && (lastP.stage === 'scanning' || lastP.stage === 'pairing');
					pollN++;
					if (!lastP || busyStage || pollN % 5 === 0) {
						api(token, port, 'GET', '/pairing/status').then(function (p) {
							if (g !== gen) return; // body was rebuilt since this poll started
							lastP = p;
							var key = JSON.stringify([p.stage, p.error, p.devices, selMac, pairMsg]);
							if (key !== pairKey || !pairCard) { pairKey = key; pairCard = buildPairCard(p); }
							body.appendChild(pairCard);
						}).catch(function () {});
					} else if (pairCard) {
						body.appendChild(pairCard);
					}
					return;
				}
				conn.className = 'wl-pill on'; conn.textContent = _('Connected');
				if (!extraLoaded) { extraLoaded = true; fetchExtras(true); }
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
		refreshAdmin();
		poll.add(refresh, 2);
		poll.add(refreshAdmin, 10);
		poll.add(updateCountdown, 1);
		return wrap;
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});
