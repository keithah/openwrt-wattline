/* Native GL-panel (oui) view for Wattline. oui loads a view via
   `const component = eval(res.data)`, so this file EVALUATES to a Vue 2
   component. Auths via the panel session (POST /rpc, sid = Admin-Token cookie)
   to wattline.get_config -> {token,port}, then polls the daemon REST API
   directly (CORS-enabled). No login. User-facing copy follows the
   LinkPower-2 manual (runtime, Bypass, USB-C charge-only, Starlink reserve). */
(function () {
  var GREEN = '#25b45f', ORANGE = '#f5a623', GREY = '#9aa0a6', RED = '#e5533c';

  function cookie(n) {
    var m = document.cookie.match(new RegExp('(?:^|; )' + n + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  }
  function rpc(obj, method, args) {
    return fetch('/rpc', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'call',
        params: [cookie('Admin-Token'), obj, method, args || {}] })
    }).then(function (r) { return r.json(); }).then(function (j) {
      if (j.error) throw new Error(j.error.message); return j.result;
    });
  }
  function flow(s) { return s === -1 ? ORANGE : GREEN; }
  function statusWord(s) { return s === 1 ? 'Charging' : s === -1 ? 'Discharging' : 'Idle'; }
  function hm(min) {
    if (min == null || min <= 0) return null;
    var h = Math.floor(min / 60), m = min % 60;
    return (h > 0 ? h + ' h ' : '') + m + ' m';
  }

  return {
    name: 'wattline',
    data: function () {
      return { token: '', port: '8377', tel: null, dev: null, err: '', loaded: false,
        pairing: null, pin: '020555', selMac: '', ptick: 0, uiErr: '' };
    },
    created: function () {
      var self = this;
      rpc('wattline', 'get_config').then(function (c) {
        self.token = c.token; self.port = c.port || '8377';
        self.tick();
        self._iv = setInterval(function () { self.tick(); }, 2000);
      }).catch(function (e) { self.err = 'Panel RPC failed: ' + e.message; self.loaded = true; });
    },
    beforeDestroy: function () { if (this._iv) clearInterval(this._iv); },
    methods: {
      base: function () { return window.location.protocol + '//' + window.location.hostname + ':' + this.port + '/api/v1'; },
      get: function (path) {
        return fetch(this.base() + path, { headers: { Authorization: 'Bearer ' + this.token } })
          .then(function (r) { return r.json(); });
      },
      tick: function () {
        var self = this;
        this.get('/telemetry').then(function (t) {
          self.tel = t; self.err = ''; self.loaded = true;
          if (t.connected && !self.dev) self.get('/status').then(function (s) { self.dev = s.device || null; }).catch(function () {});
          if (!t.connected) {
            var pp = self.pairing;
            var pbusy = pp && (pp.stage === 'scanning' || pp.stage === 'pairing');
            self.ptick++;
            if (!pp || pbusy || self.ptick % 5 === 0) {
              self.get('/pairing/status').then(function (p) { self.pairing = p; }).catch(function () { self.pairing = null; });
            }
          }
        }).catch(function () { self.err = 'Daemon unreachable — is wattlined running?'; self.loaded = true; });
      },
      post: function (path, body) {
        return fetch(this.base() + path, {
          method: 'POST', headers: { Authorization: 'Bearer ' + this.token, 'Content-Type': 'application/json' },
          body: body ? JSON.stringify(body) : undefined
        }).then(function (r) {
          if (!r.ok) return r.text().then(function (t) { throw new Error((t || '').trim() || ('HTTP ' + r.status)); });
          return r;
        });
      },
      pscan: function () { var self = this;
        this.uiErr = '';
        this.post('/pairing/scan').then(function () { self.ptick = 0; self.tick(); })
          .catch(function (e) { self.uiErr = e.message; });
      },
      ppair: function () { var self = this;
        if (!this.selMac) return;
        this.uiErr = '';
        this.post('/pairing/pair', { mac: this.selMac, pin: this.pin }).then(function () { self.ptick = 0; self.tick(); })
          .catch(function (e) { self.uiErr = e.message; });
      },
      act: function (a) { var self = this;
        this.post('/device/action', { action: a })
          .then(function () { setTimeout(function () { self.tick(); }, 800); }).catch(function () {});
      }
    },
    render: function (h) {
      var self = this;
      var el = function (tag, style, kids) { return h(tag, { style: style }, kids); };
      var card = function (kids) { return el('div', { background: '#fff', borderRadius: '16px', padding: '18px',
        margin: '12px 0', boxShadow: '0 1px 4px rgba(0,0,0,.08)', maxWidth: '460px' }, kids); };
      var big = function (v, u, c) { return el('div', { fontSize: '30px', fontWeight: '600', color: c },
        [String(v), h('span', { style: { fontSize: '16px', color: GREY, fontWeight: '400', marginLeft: '3px' } }, u)]); };
      var sub = function (txt, c) { return el('div', { color: c || GREY, fontSize: '13px' }, txt); };
      var metric = function (v, l) { return el('div', { marginRight: '24px' },
        [h('b', { style: { fontSize: '18px' } }, v), el('div', { color: GREY, fontSize: '12px' }, l)]); };
      var pill = function (txt, c, bg) { return h('span', { style: { fontSize: '11px', padding: '2px 9px',
        borderRadius: '10px', background: bg, color: c, marginLeft: '8px', verticalAlign: 'middle' } }, txt); };
      var sw = function (on, action) { return h('div', {
        style: { width: '46px', height: '26px', borderRadius: '13px', cursor: 'pointer', flex: 'none',
          background: on ? GREEN : '#d0d4d9', position: 'relative' }, on: { click: function () { self.act(action); } } },
        [el('div', { position: 'absolute', top: '3px', left: on ? '23px' : '3px', width: '20px', height: '20px',
          borderRadius: '50%', background: '#fff', transition: 'left .15s', boxShadow: '0 1px 2px rgba(0,0,0,.3)' })]); };
      var cardhead = function (title, right) { return el('div', { display: 'flex', justifyContent: 'space-between',
        alignItems: 'center', marginBottom: '2px' }, [el('div', { fontSize: '15px', fontWeight: '600', color: '#3c4043' }, title), right || '']); };

      var conn = self.tel && self.tel.connected;
      var devline = self.dev ? ('Link-Power' + (self.dev.model ? ' · ' + self.dev.model : '') + (self.dev.firmware ? ' · fw ' + self.dev.firmware : '')) : 'Link-Power';
      var header = el('div', { display: 'flex', alignItems: 'center', justifyContent: 'space-between', maxWidth: '460px' }, [
        el('div', {}, [ h('h2', { style: { margin: '0' } }, 'Wattline'),
          el('div', { color: GREY, fontSize: '12px' }, devline) ]),
        pill(conn ? 'Connected' : 'Disconnected', conn ? GREEN : GREY, conn ? '#e6f6ec' : '#eef1f4')
      ]);
      var wrap = function (kids) { return el('div', { padding: '4px 0',
        fontFamily: '-apple-system,Segoe UI,Roboto,sans-serif', color: '#202124' }, [header].concat(kids)); };

      if (!self.loaded) return wrap([sub('Loading…')]);
      if (self.err) return wrap([card([el('div', { color: GREY, textAlign: 'center', padding: '30px 0' }, self.err)])]);
      if (!conn) {
        var p = self.pairing || { stage: 'idle', devices: [] };
        var busy = p.stage === 'scanning' || p.stage === 'pairing';
        var btn = function (label, onclick, primary, disabled) {
          return h('button', { style: {
            padding: '8px 18px', borderRadius: '8px', fontSize: '14px', cursor: disabled ? 'default' : 'pointer',
            border: primary ? 'none' : '1px solid #d0d4d9', opacity: disabled ? '.5' : '1',
            background: primary ? GREEN : '#fff', color: primary ? '#fff' : '#3c4043'
          }, attrs: { disabled: !!disabled }, on: { click: onclick } }, label);
        };
        var rows = (p.devices || []).map(function (d) {
          var selected = self.selMac === d.mac;
          return h('div', { key: d.mac, style: {
            display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '10px 12px',
            borderRadius: '10px', cursor: 'pointer', marginTop: '6px',
            border: selected ? '2px solid ' + GREEN : '1px solid #e4e7eb'
          }, on: { click: function () { self.selMac = d.mac; } } }, [
            el('div', {}, [ h('b', { style: { fontSize: '14px' } }, d.name || '(unnamed)'),
              el('div', { color: GREY, fontSize: '12px' }, d.mac + (d.paired ? ' · previously paired' : '')) ]),
            el('div', { color: GREY, fontSize: '12px' }, d.rssi ? d.rssi + ' dBm' : '')
          ]);
        });
        var pairBits = [
          cardhead('Pair your Link-Power'),
          sub('Power on the Link-Power, keep it near the router, then scan. Make sure no phone or laptop app is connected to it.'),
          el('div', { marginTop: '12px' }, [ btn(p.stage === 'scanning' ? 'Scanning…' : 'Scan for devices', function () { self.pscan(); }, false, busy) ])
        ];
        if (rows.length) pairBits.push(el('div', { marginTop: '6px' }, rows));
        if (self.selMac) {
          pairBits.push(el('div', { display: 'flex', alignItems: 'center', marginTop: '12px' }, [
            el('div', { marginRight: '10px' }, [ sub('PIN'),
              h('input', { style: { width: '90px', padding: '7px 9px', fontSize: '14px', border: '1px solid #d0d4d9',
                borderRadius: '8px', marginTop: '2px' },
                attrs: { maxlength: 6, inputmode: 'numeric' },
                domProps: { value: self.pin },
                on: { input: function (e) {
                  var v = e.target.value.replace(/[^0-9]/g, '').slice(0, 6);
                  e.target.value = v; self.pin = v;
                } } }) ]),
            el('div', { flex: '1' }, ''),
            btn(p.stage === 'pairing' ? 'Pairing…' : 'Pair', function () { self.ppair(); }, true, busy)
          ]));
          pairBits.push(el('div', { color: GREY, fontSize: '12px', marginTop: '6px' },
            'Default PIN is 020555 (see the manual). If the device shows a PIN on its screen, enter that instead.'));
        }
        if (self.uiErr) pairBits.push(el('div', { color: RED, fontSize: '13px', marginTop: '10px' }, self.uiErr));
        if (p.stage === 'pairing') pairBits.push(el('div', { color: ORANGE, fontSize: '13px', marginTop: '10px' }, 'Pairing and verifying the connection… this usually takes under a minute.'));
        if (p.stage === 'paired') pairBits.push(el('div', { color: GREEN, fontSize: '13px', marginTop: '10px' }, 'Paired. Connecting…'));
        if (p.stage === 'error') pairBits.push(el('div', { color: RED, fontSize: '13px', marginTop: '10px' }, 'Pairing failed: ' + (p.error || 'unknown error')));
        return wrap([
          card([el('div', { textAlign: 'center', padding: '10px 10px 14px', color: GREY }, [
            el('div', { fontSize: '15px', color: '#3c4043', marginBottom: '6px' }, 'No power bank connected'),
            'Plug the USB BLE dongle into the router and power on the Link-Power. Already-paired devices connect automatically.'
          ])]),
          card(pairBits)
        ]);
      }

      var t = self.tel, b = t.battery || {}, dc = t.dc || {}, c = t.typec || {};
      var bColor = flow(b.status);
      // Battery ring
      var r = 74, circ = 2 * Math.PI * r, off = circ * (1 - (b.level || 0) / 100);
      var ring = el('div', { position: 'relative', width: '170px', height: '170px' }, [
        h('svg', { attrs: { width: 170, height: 170, viewBox: '0 0 170 170' }, style: { transform: 'rotate(-90deg)' } }, [
          h('circle', { attrs: { cx: 85, cy: 85, r: r, fill: 'none', stroke: '#eef1f4', 'stroke-width': 12 } }),
          h('circle', { attrs: { cx: 85, cy: 85, r: r, fill: 'none', stroke: bColor, 'stroke-width': 12, 'stroke-linecap': 'round',
            'stroke-dasharray': circ.toFixed(1), 'stroke-dashoffset': off.toFixed(1) } })
        ]),
        el('div', { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, display: 'flex',
          flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }, [
          h('b', { style: { fontSize: '46px', color: bColor } }, String(b.level != null ? b.level : 0)),
          h('span', { style: { fontSize: '15px', color: GREY } }, '%')
        ])
      ]);
      // Runtime line (the number Starlink users care about)
      var rt = hm(b.remain_min);
      var runtimeTxt = b.full ? 'Fully charged'
        : (rt ? (rt + (b.status === 1 ? ' to full' : ' remaining')) : (statusWord(b.status)));
      var battery = card([
        el('div', { display: 'flex', justifyContent: 'center', padding: '4px 0 10px' }, [ring]),
        el('div', { textAlign: 'center' }, [
          el('div', { fontSize: '18px', fontWeight: '600', color: bColor }, runtimeTxt),
          sub(statusWord(b.status) + ' · ' + (b.wh != null ? b.wh.toFixed(1) : '—') + ' / ' + (b.max_wh != null ? b.max_wh.toFixed(0) : '—') + ' Wh'
            + (b.volts != null ? ' · ' + b.volts.toFixed(1) + ' V' : ''))
        ])
      ]);

      var dcOn = !!dc.enabled;
      var dcCard = card([
        cardhead(['DC Port', dc.bypass ? pill('Bypass on', GREEN, '#e6f6ec') : ''], sw(dcOn, dcOn ? 'dc_off' : 'dc_on')),
        sub('Powers your Starlink Mini'),
        el('div', { marginTop: '6px' }, [big(dc.watts != null ? dc.watts.toFixed(1) : '—', 'W', flow(dc.status)),
          sub(dc.status === 1 ? 'Charging power' : 'Output power')]),
        el('div', { display: 'flex', marginTop: '10px', alignItems: 'flex-end' }, [
          metric((dc.volts != null ? dc.volts.toFixed(1) : '—') + ' V', 'Voltage'),
          metric((dc.amps != null ? dc.amps.toFixed(2) : '—') + ' A', 'Current'),
          el('div', { flex: '1' }, ''),
          el('div', { textAlign: 'right' }, [ sub('Bypass'),
            el('div', { marginTop: '3px', display: 'inline-block' }, [sw(!!dc.bypass, dc.bypass ? 'bypass_off' : 'bypass_on')]) ])
        ])
      ]);

      var cMode = c.mode || 0, cOut = (cMode === 2 || cMode === 3);
      var modeTxt = cMode === 3 ? 'Charge & Discharge' : cMode === 1 ? 'Charging only' : cMode === 2 ? 'Output only' : 'Off';
      var tempHigh = c.temp_c != null && c.temp_c >= 55;
      var cCard = card([
        cardhead(['USB-C Port', pill(modeTxt, '#5f6368', '#eef1f4')], sw(cOut, cOut ? 'usbc_off' : 'usbc_on')),
        el('div', { marginTop: '6px' }, [big(c.watts != null ? c.watts.toFixed(1) : '—', 'W', flow(c.status)),
          sub(c.status === 1 ? 'Charging power' : c.status === -1 ? 'Output power' : 'Idle')]),
        el('div', { display: 'flex', marginTop: '10px' }, [
          metric((c.volts != null ? c.volts.toFixed(1) : '—') + ' V', 'Voltage'),
          metric((c.amps != null ? c.amps.toFixed(2) : '—') + ' A', 'Current'),
          metric(h('span', { style: { color: tempHigh ? RED : '#202124' } }, (c.temp_c != null ? c.temp_c.toFixed(0) : '—') + ' °C'), 'Temp')
        ])
      ]);

      var note = el('div', { color: GREY, fontSize: '12px', maxWidth: '460px', margin: '6px 2px 0', lineHeight: '1.5' },
        '~10–15% of the battery is reserved for the Starlink Mini — USB-C output turns off automatically below that to keep your dish running.');

      return wrap([battery, dcCard, cCard, note]);
    }
  };
})()
