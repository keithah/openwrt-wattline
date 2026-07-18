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
  function apiError(response) {
    return response.json().then(function (payload) {
      var detail = payload && payload.error;
      var error = new Error(detail && detail.message ? detail.message : ('HTTP ' + response.status));
      error.code = detail && detail.code; error.status = response.status;
      throw error;
    }).catch(function (error) {
      if (error && error.status) throw error;
      var fallback = new Error('HTTP ' + response.status); fallback.status = response.status; throw fallback;
    });
  }
  function apiClient(config, token) {
    var host = window.location.hostname;
    var endpoints = [];
    var preferred = null;
    if (config.https_enabled) endpoints.push('https://' + host + ':' + config.https_port + '/api/v1');
    if (config.http_enabled) endpoints.push('http://' + host + ':' + config.port + '/api/v1');
    function response(method, path, body, extra) {
      method = method.toUpperCase();
      var safe = method === 'GET';
      // A failed GET is safe to probe over HTTP. A mutation may already have
      // committed before a connection error, so it is sent exactly once over
      // the listener already proven by a completed HTTP response.
      var candidates;
      if (safe) {
        candidates = preferred ? [preferred].concat(endpoints.filter(function (endpoint) {
          return endpoint !== preferred;
        })) : endpoints.slice();
      } else {
        candidates = preferred ? [preferred] : (endpoints.length ? [endpoints[0]] : []);
      }
      var index = 0;
      function attempt(lastError) {
        if (index >= candidates.length) return Promise.reject(lastError || new Error('No API listener is enabled'));
        var endpoint = candidates[index++];
        var headers = { Authorization: 'Bearer ' + token };
        if (body != null) headers['Content-Type'] = 'application/json';
        var options = { method: method, headers: headers, cache: 'no-store', body: body == null ? null : JSON.stringify(body) };
        if (extra && extra.signal) options.signal = extra.signal;
        return fetch(endpoint + path, options).then(function (result) {
          preferred = endpoint;
          return result;
        }).catch(function (error) {
          var aborted = error && error.name === 'AbortError';
          if (safe && !aborted && index < candidates.length) return attempt(error);
          throw error;
        });
      }
      return attempt();
    }
    function checked(method, path, body, extra) {
      return response(method, path, body, extra).then(function (r) { return r.ok ? r : apiError(r); });
    }
    return {
      json: function (method, path, body, extra) { return checked(method, path, body, extra).then(function (r) { return r.json(); }); },
      blob: function (method, path, body, extra) { return checked(method, path, body, extra).then(function (r) { return r.blob(); }); }
    };
  }
  function powerLossCompatible(rule) {
    return !!rule && rule.name === 'no_input_shutdown' && rule.condition === 'input_power' &&
      rule.state === 'absent' && Array.isArray(rule.actions) && rule.actions.indexOf('shutdown') !== -1;
  }
  function powerLossClassify(rules) {
    var list = Array.isArray(rules) ? rules : [], rule = null;
    for (var i = 0; i < list.length; i++) {
      if (list[i] && list[i].name === 'no_input_shutdown') { rule = list[i]; break; }
    }
    if (!rule) return { kind: 'missing', rule: null };
    return { kind: powerLossCompatible(rule) ? 'compatible' : 'conflict', rule: rule };
  }
  function powerLossPayload(existing, enabled, minutes, reset) {
    var delay = Number(minutes);
    if (!Number.isInteger(delay) || delay < 1 || delay > 1440)
      throw new Error('Delay must be a whole number from 1 to 1440 minutes');
    if (existing && !reset && !powerLossCompatible(existing))
      throw new Error('Customized rule conflict requires resetting the preset');
    var rule = reset || !existing ? {
      name: 'no_input_shutdown', enabled: true, condition: 'input_power', state: 'absent',
      hold: 600000000000, hysteresis_margin: 5, actions: ['shutdown'], confirm_shutdown: true
    } : Object.assign({}, existing);
    rule.name = 'no_input_shutdown'; rule.enabled = !!enabled;
    rule.hold = delay * 60000000000; rule.confirm_shutdown = true;
    return rule;
  }
  function powerLossMinutes(rule) {
    var value = Number(rule && rule.hold) / 60000000000;
    if (!Number.isFinite(value) || value <= 0) return 10;
    return Math.max(1, Math.min(1440, Math.round(value)));
  }
  function powerLossDurationSeconds(value) {
    var match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(String(value || ''));
    if (!match || (!match[1] && !match[2] && !match[3])) return null;
    return Number(match[1] || 0) * 3600 + Number(match[2] || 0) * 60 + Number(match[3] || 0);
  }
  function powerLossDisplay(rule, status, telemetry) {
    var model = powerLossClassify(rule ? [rule] : []);
    if (model.kind === 'missing') return { kind: 'missing', remainingSeconds: null };
    if (model.kind === 'conflict') return { kind: 'conflict', remainingSeconds: null };
    if (rule.enabled === false) return { kind: 'disabled', remainingSeconds: null };
    if (!telemetry || telemetry.connected !== true) return { kind: 'disconnected', remainingSeconds: null };
    var inputPresent = !!((telemetry.battery && telemetry.battery.status === 1) ||
      (telemetry.typec && telemetry.typec.dc_input === true));
    if (inputPresent) return { kind: 'present', remainingSeconds: null };
    var list = Array.isArray(status) ? status : status && status.rules, runtime = null;
    if (Array.isArray(list)) {
      for (var i = 0; i < list.length; i++) {
        if (list[i] && list[i].name === 'no_input_shutdown') { runtime = list[i]; break; }
      }
    }
    if (runtime && runtime.armed === false && runtime.last_fired)
      return { kind: 'fired', remainingSeconds: 0, lastFired: runtime.last_fired };
    var elapsed = runtime && runtime.armed === true ? powerLossDurationSeconds(runtime.holding_for) : null;
    var total = Number(rule.hold) / 1000000000;
    if (elapsed == null || !Number.isFinite(total) || total <= 0)
      return { kind: 'inactive', remainingSeconds: null };
    return { kind: 'holding', remainingSeconds: Math.max(0, Math.round(total - elapsed)) };
  }
  function advancedCapabilities(settings, device) {
    var enabled = !!(settings && settings.advanced), available = device && device.available || {};
    var features = device && device.features || {};
    return {
      ota: enabled && available.ota === true,
      clock: enabled && available.current_time === true,
      runningMode: enabled && features.running_mode === true,
      barrierFree: enabled && features.barrier_free === true,
      usbFirmware: enabled && features.usb_firmware === true,
      blePIN: enabled && features.ble_pin === true
    };
  }
  function timerInputKind(type) {
    return Number(type) === 0 ? 'date' : Number(type) === 2 ? 'weekly_mask' : Number(type) === 3 ? 'monthly_mask' : 'none';
  }
  function timerStatusLabel(status) {
    return status === 1 ? 'enabled' : status === -1 ? 'disabled' : status === -2 ? 'validation-disabled' : status === -3 ? 'expired' : 'unknown';
  }
  function timerActionLabel(action) { return Number(action) === 1 ? 'On' : 'Off'; }
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
      return { token: '', config: null, client: null, tel: null, dev: null, err: '', loaded: false,
        pairing: null, pin: '020555', selMac: '', ptick: 0, uiErr: '',
        usbcLimit: null, threshold: null, thrInput: '', schedules: null,
        etick: 0, ctlErr: '', newSch: { status: 1, type: 1, hour: 8, minute: 0, action: 1, date: '', repeatInput: '' },
        adminTick: 0, settings: null, tokens: [], apiPair: null, adminErr: '', qrURL: '', qrCtl: null,
        settingsDraft: null, adminGeneration: 0, adminMutations: 0, destroyed: false,
        powerLossRule: null, powerLossStatus: null,
        powerLossDraft: { enabled: false, minutes: '10', focused: false },
        powerLossBusy: false, powerLossRetryDraft: false, powerLossError: '',
        usbFirmware: '', barrierFree: null, commandBusy: {} };
    },
    created: function () {
      var self = this;
      rpc('wattline', 'get_config').then(function (c) {
        c.http_enabled = c.http_enabled !== false; c.https_enabled = c.https_enabled !== false;
        c.port = c.port || '8377'; c.https_port = c.https_port || '8378';
        self.token = c.token; self.config = c; self.client = apiClient(c, c.token);
        self.tick();
        self._iv = setInterval(function () { self.tick(); }, 2000);
      }).catch(function (e) { self.err = 'Panel RPC failed: ' + e.message; self.loaded = true; });
    },
    beforeDestroy: function () {
      this.destroyed = true; this.adminGeneration++;
      if (this._iv) clearInterval(this._iv);
      this.clearQR();
    },
    methods: {
      get: function (path) { return this.client.json('GET', path); },
      mutate: function (method, path, body) { return this.client.json(method, path, body); },
      tick: function () {
        var self = this;
        this.get('/telemetry').then(function (t) {
          self.tel = t; self.err = ''; self.loaded = true;
          if (t.connected && !self.dev) self.get('/device').then(function (s) { self.dev = s; }).catch(function () {});
          if (t.connected) {
            // Refresh device settings on connect and every ~10s (they rarely change).
            if (self.usbcLimit == null || self.etick % 5 === 0) self.fetchExtras();
            self.etick++;
          } else {
            self.usbcLimit = null; self.threshold = null; self.schedules = null; self.etick = 0;
          }
          if (!t.connected) {
            var pp = self.pairing;
            var pbusy = pp && (pp.stage === 'scanning' || pp.stage === 'pairing');
            self.ptick++;
            if (!pp || pbusy || self.ptick % 5 === 0) {
              self.get('/pairing/status').then(function (p) { self.pairing = p; }).catch(function () { self.pairing = null; });
            }
          }
          self.adminTick++;
          if (!self.settings || self.adminTick % 5 === 0) self.fetchAdmin();
        }).catch(function () { self.err = 'Daemon unreachable — is wattlined running?'; self.loaded = true; });
      },
      post: function (path, body) {
        return this.mutate('POST', path, body);
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
      act: function (a) { var self = this, operation;
        if (this.isPending(a)) return;
        this.commandBusy[a] = true; if (this.$forceUpdate) this.$forceUpdate();
        if (a === 'dc_on' || a === 'dc_off') operation = this.post('/device/dc', { on: a === 'dc_on' });
        else if (a === 'usbc_on' || a === 'usbc_off') operation = this.post('/device/usbc/output', { on: a === 'usbc_on' });
        else if (a === 'bypass_on' || a === 'bypass_off') operation = this.post('/device/dc/bypass', { on: a === 'bypass_on' });
        else if (a === 'restart') operation = this.post('/device/restart');
        else if (a === 'shutdown') operation = this.post('/device/shutdown', { confirm: true });
        else operation = this.post('/device/action', { action: a });
        return operation.then(function () {
          delete self.commandBusy[a]; if (self.$forceUpdate) self.$forceUpdate();
          self.ctlErr = ''; setTimeout(function () { self.tick(); }, 800);
        }).catch(function (error) {
          delete self.commandBusy[a]; if (self.$forceUpdate) self.$forceUpdate();
          self.ctlErr = error.message;
        });
      },
      isPending: function (action) {
        var operations = { dc_on: 'dc_output', dc_off: 'dc_output', usbc_on: 'usbc_output', usbc_off: 'usbc_output',
          bypass_on: 'dc_bypass', bypass_off: 'dc_bypass', restart: 'restart', shutdown: 'shutdown' };
        var operation = operations[action];
        if (this.commandBusy && this.commandBusy[action]) return true;
        return !!operation && !!(this.dev && this.dev.commands && this.dev.commands.active || []).some(function (command) {
          return command.operation === operation && command.phase === 'pending';
        });
      },
      fetchExtras: function () { var self = this;
        this.get('/device/usbc/limit/output').then(function (l) { self.usbcLimit = { output: l }; }).catch(function () {});
        if (this.settings && this.settings.advanced && this.dev && this.dev.features && this.dev.features.dc_bypass_control) {
          this.get('/device/dc/bypass/threshold').then(function (t) {
            self.threshold = (t && typeof t.volts === 'number') ? t.volts : null;
            if (self.threshold != null && self.thrInput === '') self.thrInput = String(self.threshold);
          }).catch(function () {});
        }
        this.get('/device/timers').then(function (s) { self.schedules = Array.isArray(s) ? s : (s.timers || []); }).catch(function () {});
      },
      setLimit: function (watts) { var self = this;
        this.ctlErr = '';
        this.mutate('PUT', '/device/usbc/limit/output', { watts: watts })
          .then(function () { self.fetchExtras(); }).catch(function (e) { self.ctlErr = e.message; });
      },
      setThreshold: function () { var self = this;
        this.ctlErr = '';
        var v = parseFloat(this.thrInput);
        if (!(v > 0)) { this.ctlErr = 'Enter a voltage'; return; }
        this.mutate('PUT', '/device/dc/bypass/threshold', { volts: v })
          .then(function () { self.fetchExtras(); }).catch(function (e) { self.ctlErr = e.message; });
      },
      addSched: function () { var self = this;
        this.ctlErr = '';
        var payload;
        try { payload = this.timerPayload(this.newSch); } catch (error) { this.ctlErr = error.message; return; }
        this.post('/device/timers', payload)
          .then(function () { self.fetchExtras(); }).catch(function (e) { self.ctlErr = e.message; });
      },
      timerPayload: function (input) {
        var status = Number(input.status), type = Number(input.type), hour = Number(input.hour);
        var minute = Number(input.minute), action = Number(input.action), repeat = 0;
        if (status !== 1 && status !== -1) throw new Error('Timer status must be enabled or disabled');
        if (!Number.isInteger(type) || type < 0 || type > 3 || !Number.isInteger(hour) || hour < 0 || hour > 23 ||
          !Number.isInteger(minute) || minute < 0 || minute > 59 || (action !== 0 && action !== 1)) throw new Error('Invalid timer fields');
        if (type === 0) {
          var match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(input.date || '');
          if (!match) throw new Error('One-shot timer needs a valid date');
          var year = Number(match[1]), month = Number(match[2]), day = Number(match[3]);
          var date = new Date(0); date.setUTCHours(0, 0, 0, 0); date.setUTCFullYear(year, month - 1, day);
          if (year < 1 || year > 65535 || date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day)
            throw new Error('One-shot timer needs a valid date');
          repeat = ((day << 24) | (month << 16) | year) >>> 0;
        } else if (type > 1) {
          if (!/^\d+$/.test(String(input.repeatInput || ''))) throw new Error('Timer repeat mask must be decimal');
          repeat = Number(input.repeatInput);
          var maximum = type === 2 ? 254 : 4294967294;
          if (!Number.isSafeInteger(repeat) || repeat < 2 || repeat > maximum || repeat % 2 !== 0)
            throw new Error('Timer repeat mask uses bits 1 and above');
        }
        return { status: status, type: type, hour: hour, minute: minute, repeat: repeat, action: action };
      },
      delSched: function (id) { var self = this;
        this.ctlErr = '';
        if (!window.confirm('Delete this on-device timer?')) return;
        this.mutate('DELETE', '/device/timers/' + id)
          .then(function () { self.fetchExtras(); })
          .catch(function (e) { self.ctlErr = e.message; });
      },
      confirmAct: function (a, msg) { var self = this;
        if (window.confirm(msg)) self.act(a);
      },
      focused: function () {
        var active = document.activeElement;
        return active && /^(INPUT|SELECT|TEXTAREA)$/.test(active.tagName);
      },
      fetchAdmin: function () { var self = this;
        if (this.destroyed || this.adminMutations) return Promise.resolve(null);
        var generation = ++this.adminGeneration;
        return Promise.all([this.get('/device'), this.get('/settings'), this.get('/tokens'), this.get('/pairing-mode'),
          this.get('/rules'), this.get('/status')])
          .then(function (values) {
            if (self.destroyed || self.adminMutations || generation !== self.adminGeneration) return null;
            self.dev = values[0]; self.settings = values[1]; self.tokens = values[2] || []; self.apiPair = values[3];
            var powerModel = self.powerLossClassify(values[4]);
            self.powerLossRule = powerModel.rule; self.powerLossStatus = values[5];
            if (!self.powerLossRetryDraft) {
              self.powerLossDraft.enabled = powerModel.rule ? powerModel.rule.enabled !== false : true;
              if (!self.powerLossDraft.focused) self.powerLossDraft.minutes = String(powerLossMinutes(powerModel.rule));
            }
            // Polls update read-only state but never replace a user's focused
            // reachability form. The draft is refreshed after explicit saves.
            if (!self.settingsDraft && !self.focused()) self.settingsDraft = JSON.parse(JSON.stringify(values[1]));
            self.adminErr = '';
            if (values[3] && values[3].open) self.loadQR(values[3].expires_at); else self.clearQR();
          }).catch(function (error) {
            if (!self.destroyed && !self.adminMutations && generation === self.adminGeneration) self.adminErr = error.message;
            return null;
          });
      },
      adminAction: function (method, path, body, prompt) { var self = this;
        if (prompt && !window.confirm(prompt)) return;
        if (this.destroyed || this.adminMutations) return Promise.resolve(null);
        this.adminMutations++; this.adminGeneration++; if (this.$forceUpdate) this.$forceUpdate();
        if (method === 'DELETE' && path === '/pairing-mode') this.clearQR();
        function finish(value, failed) {
          self.adminMutations--; if (self.$forceUpdate) self.$forceUpdate();
          if (self.destroyed) return failed ? Promise.reject(value) : value;
          return self.fetchAdmin().then(function () {
            if (failed) throw value;
            return value;
          });
        }
        return Promise.resolve().then(function () { return self.mutate(method, path, body); })
          .then(function (value) { return finish(value, false); }, function (error) { return finish(error, true); })
          .catch(function (error) { if (!self.destroyed) self.adminErr = error.message; return null; });
      },
      clearQR: function () {
        if (this.qrCtl) this.qrCtl.abort(); this.qrCtl = null;
        if (this.qrURL) URL.revokeObjectURL(this.qrURL); this.qrURL = '';
      },
      loadQR: function (expiry) { var self = this;
        if (this._qrExpiry === expiry && this.qrURL) return;
        this.clearQR(); this._qrExpiry = expiry;
        var controller = new AbortController(); this.qrCtl = controller;
        this.client.blob('GET', '/pairing-mode/qr.png', null, { signal: controller.signal }).then(function (blob) {
          if (self.destroyed || controller.signal.aborted || self.qrCtl !== controller) return;
          self.qrURL = URL.createObjectURL(blob); self.qrCtl = null;
        }).catch(function (error) { if (!self.destroyed && error.name !== 'AbortError') self.adminErr = error.message; });
      },
      saveSettings: function () {
        if (!this.settingsDraft) return;
        var d = this.settingsDraft;
        if (!d.http.enabled && !d.https.enabled) { this.adminErr = 'At least one HTTP or HTTPS listener must be enabled'; return; }
        if (d.wan_access && !(this.settings && this.settings.wan_access) &&
          !window.confirm('Enable WAN access? This is insecure — use TLS/VPN and strong client tokens.')) return;
        if (d.pairing_always_on && !(this.settings && this.settings.pairing_always_on) &&
          !window.confirm('Make API-client pairing always available to anyone with the PIN?')) return;
        if (!window.confirm('Save reachability settings? Listener, TLS, mDNS, and WAN changes require restarting wattlined.')) return;
        // PUT has merge semantics. Never echo the read-only TLS fingerprint or
        // storage paths returned by GET; submit only fields this panel edits.
        var update = { http: d.http, https: d.https, pairing_ttl: d.pairing_ttl,
          pairing_always_on: d.pairing_always_on, mdns: d.mdns, wan_access: d.wan_access };
        var operatorDraft = JSON.parse(JSON.stringify(d));
        var self = this;
        this.settingsDraft = null;
        return this.adminAction('PUT', '/settings', update).then(function (result) {
          // adminAction performs the authoritative refresh before returning.
          // Preserve its fresh read-only policy state, but reinstate the
          // operator's editable values when the PUT itself failed.
          if (result === null && !self.destroyed) self.settingsDraft = operatorDraft;
          return result;
        });
      },
      powerLossClassify: function (rules) { return powerLossClassify(rules); },
      powerLossPayload: function (existing, enabled, minutes, reset) {
        return powerLossPayload(existing, enabled, minutes, reset);
      },
      powerLossDisplay: function (rule, status, telemetry) {
        return powerLossDisplay(arguments.length ? rule : this.powerLossRule,
          arguments.length > 1 ? status : this.powerLossStatus,
          arguments.length > 2 ? telemetry : this.tel);
      },
      savePowerLoss: function (reset) { var self = this;
        if (this.destroyed || this.powerLossBusy || this.adminMutations) return Promise.resolve(null);
        var model = this.powerLossClassify(this.powerLossRule ? [this.powerLossRule] : []);
        if (model.kind === 'conflict' && !reset) {
          this.powerLossError = 'Customized rule conflict. Save is disabled until you reset this preset.';
          return Promise.resolve(null);
        }
        if (reset && !window.confirm('Reset the customized rule to the Power-loss shutdown preset?'))
          return Promise.resolve(null);
        var submitted = { enabled: !!this.powerLossDraft.enabled, minutes: String(this.powerLossDraft.minutes),
          focused: !!this.powerLossDraft.focused }, payload;
        try { payload = this.powerLossPayload(model.rule, submitted.enabled, submitted.minutes, reset); }
        catch (error) { this.powerLossError = error.message; return Promise.resolve(null); }
        var method = model.kind === 'missing' ? 'POST' : 'PUT';
        var path = model.kind === 'missing' ? '/rules' : '/rules/no_input_shutdown';
        this.powerLossBusy = true; this.powerLossRetryDraft = false; this.powerLossError = '';
        if (this.$forceUpdate) this.$forceUpdate();
        return this.adminAction(method, path, payload).then(function (result) {
          self.powerLossBusy = false;
          if (result === null && !self.destroyed) {
            self.powerLossDraft = submitted;
            self.powerLossRetryDraft = true;
            self.powerLossError = self.adminErr || 'Power-loss preset update failed';
          } else { self.powerLossRetryDraft = false; self.powerLossError = ''; }
          if (self.$forceUpdate) self.$forceUpdate();
          return result;
        });
      },
      setAdvanced: function (enabled) {
        this.adminAction('PUT', '/settings', { advanced: enabled }, enabled ? 'Enable advanced controls? These operations can change device firmware modes and BLE access.' : null);
      }
    },
    render: function (h) {
      var self = this;
      var el = function (tag, style, kids) { return h(tag, { style: style }, kids); };
      var card = function (kids) { return el('div', { background: 'rgba(255,255,255,.96)', borderRadius: '4px', padding: '17px',
        margin: '10px 0', border: '1px solid #d9dfe2', borderLeft: '3px solid #667078', maxWidth: '460px' }, kids); };
      var big = function (v, u, c) { return el('div', { fontSize: '30px', fontWeight: '600', color: c },
        [String(v), h('span', { style: { fontSize: '16px', color: GREY, fontWeight: '400', marginLeft: '3px' } }, u)]); };
      var sub = function (txt, c) { return el('div', { color: c || GREY, fontSize: '13px' }, txt); };
      // v may be a string or a vnode (e.g. the coloured temp span); Vue 2 needs
      // children as an array, so wrap it — a bare vnode child renders blank.
      var metric = function (v, l) { return el('div', { marginRight: '24px' },
        [h('b', { style: { fontSize: '18px' } }, [v]), el('div', { color: GREY, fontSize: '12px' }, l)]); };
      var pill = function (txt, c, bg) { return h('span', { style: { fontSize: '11px', padding: '2px 9px',
        borderRadius: '10px', background: bg, color: c, marginLeft: '8px', verticalAlign: 'middle' } }, txt); };
      var sw = function (on, action) {
        var pending = self.isPending(action);
        var label = (action.indexOf('usbc') === 0 ? 'USB-C output' : action.indexOf('bypass') === 0 ? 'DC bypass' : 'DC output') +
          (pending ? ' command pending' : (on ? ' on' : ' off'));
        return h('button', {
        attrs: { type: 'button', 'aria-pressed': on ? 'true' : 'false', 'aria-label': label, disabled: pending },
        style: { width: '46px', height: '26px', borderRadius: '13px', cursor: pending ? 'wait' : 'pointer', flex: 'none',
          border: 'none', padding: '0', opacity: pending ? '.55' : '1',
          background: on ? GREEN : '#d0d4d9', position: 'relative' }, on: { click: function () { if (!pending) self.act(action); } } },
        [el('div', { position: 'absolute', top: '3px', left: on ? '23px' : '3px', width: '20px', height: '20px',
          borderRadius: '50%', background: '#fff', transition: 'left .15s', boxShadow: '0 1px 2px rgba(0,0,0,.3)' })]);
      };
      var cardhead = function (title, right) { return el('div', { display: 'flex', justifyContent: 'space-between',
        alignItems: 'center', marginBottom: '2px' }, [el('div', { fontSize: '15px', fontWeight: '600', color: '#3c4043' }, title), right || '']); };

      var conn = self.tel && self.tel.connected;
      var devline = self.dev ? ('Link-Power' + (self.dev.model ? ' · ' + self.dev.model : '') +
        (self.dev.application_firmware ? ' · fw ' + self.dev.application_firmware : '')) : 'Link-Power';
      var header = el('div', { display: 'flex', alignItems: 'center', justifyContent: 'space-between', maxWidth: '460px' }, [
        el('div', {}, [ h('h2', { style: { margin: '0' } }, 'Wattline'),
          el('div', { color: GREY, fontSize: '12px' }, devline) ]),
        pill(conn ? 'Connected' : 'Disconnected', conn ? GREEN : GREY, conn ? '#e6f6ec' : '#eef1f4')
      ]);
      var wrap = function (kids) { return el('div', { padding: '10px 8px 30px',
        fontFamily: 'Avenir Next,Avenir,Helvetica Neue,sans-serif', color: '#202124',
        backgroundImage: 'linear-gradient(rgba(54,65,71,.035) 1px,transparent 1px)', backgroundSize: '100% 24px' }, [header].concat(kids)); };

      if (!self.loaded) return wrap([sub('Loading…')]);
      if (self.err) return wrap([card([el('div', { color: GREY, textAlign: 'center', padding: '30px 0' }, self.err)])]);
      var offlineCards = null;
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
        offlineCards = [
          card([el('div', { textAlign: 'center', padding: '10px 10px 14px', color: GREY }, [
            el('div', { fontSize: '15px', color: '#3c4043', marginBottom: '6px' }, 'No power bank connected'),
            'Plug the USB BLE dongle into the router and power on the Link-Power. Already-paired devices connect automatically.'
          ])]),
          card(pairBits)
        ];
      }

      var t = self.tel || {}, b = t.battery || {}, dc = t.dc || {}, c = t.typec || {};
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
          self.dev && self.dev.features && self.dev.features.dc_bypass_control ? el('div', { textAlign: 'right' }, [ sub('Bypass'),
            el('div', { marginTop: '3px', display: 'inline-block' }, [sw(!!dc.bypass, dc.bypass ? 'bypass_off' : 'bypass_on')]) ]) : ''
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

      // --- Device settings card: USB-C output limit + DC bypass threshold ---
      var btn = function (label, onclick, kind) {
        var bg = kind === 'danger' ? RED : kind === 'primary' ? GREEN : '#fff';
        var col = kind ? '#fff' : '#3c4043';
        return h('button', { style: { padding: '7px 14px', borderRadius: '8px', fontSize: '13px',
          cursor: self.adminMutations ? 'wait' : 'pointer', opacity: self.adminMutations ? '.55' : '1',
          border: kind ? 'none' : '1px solid #d0d4d9', background: bg, color: col, marginRight: '8px' },
          attrs: { type: 'button', disabled: !!self.adminMutations },
          on: { click: onclick } }, label);
      };
      var wattChoices = [30, 45, 60, 65, 100, 140];
      var curWatts = self.usbcLimit && self.usbcLimit.output ? self.usbcLimit.output.watts : 0;
      var limitRow = el('div', { display: 'flex', flexWrap: 'wrap', alignItems: 'center', marginTop: '6px' },
        wattChoices.map(function (wv) {
          var on = curWatts === wv;
          return h('button', { style: { padding: '6px 12px', borderRadius: '8px', fontSize: '13px', cursor: 'pointer',
            marginRight: '6px', marginBottom: '6px', border: on ? '2px solid ' + GREEN : '1px solid #d0d4d9',
            background: on ? '#e6f6ec' : '#fff', color: '#3c4043' }, on: { click: function () { self.setLimit(wv); } } }, wv + ' W');
        }));
      var thrRow = el('div', { display: 'flex', alignItems: 'flex-end', marginTop: '12px' }, [
        el('div', { marginRight: '10px' }, [ sub('DC bypass engages at'),
          h('input', { style: { width: '80px', padding: '7px 9px', fontSize: '14px', border: '1px solid #d0d4d9',
            borderRadius: '8px', marginTop: '2px' }, attrs: { inputmode: 'decimal' }, domProps: { value: self.thrInput },
            on: { input: function (e) { self.thrInput = e.target.value; } } }) ]),
        sub('V', GREY), el('div', { flex: '1' }, ''), btn('Set', function () { self.setThreshold(); })
      ]);
      var thresholdAllowed = self.settings && self.settings.advanced && self.dev && self.dev.features && self.dev.features.dc_bypass_control;
      var settingsKids = [
        cardhead('Device settings'),
        sub('USB-C output power limit'),
        limitRow
      ];
      if (thresholdAllowed) settingsKids.push(thrRow);
      if (self.ctlErr) settingsKids.push(el('div', { color: RED, fontSize: '12px', marginTop: '8px' }, self.ctlErr));
      var settings = card(settingsKids);

      // --- Schedules card (on-device timers) ---
      var dayType = function (ty) { return ty === 0 ? 'Once' : ty === 1 ? 'Daily' : ty === 2 ? 'Weekly' : 'Monthly'; };
      var hhmm = function (t) { return ('0' + t.hour).slice(-2) + ':' + ('0' + t.minute).slice(-2); };
      var schRows = (self.schedules || []).map(function (t) {
        return el('div', { display: 'flex', justifyContent: 'space-between', alignItems: 'center',
          padding: '8px 0', borderTop: '1px solid #eef1f4' }, [
          el('div', {}, [ h('b', { style: { fontSize: '14px' } }, hhmm(t) + ' · ' + timerActionLabel(t.action)),
            el('div', { color: GREY, fontSize: '12px' }, dayType(t.type) + ' · ' + timerStatusLabel(t.status)) ]),
          btn('Delete', function () { self.delSched(t.id); }, 'danger')
        ]);
      });
      var ns = self.newSch;
      var numInput = function (key, w) { return h('input', { style: { width: w, padding: '6px 8px', fontSize: '14px',
        border: '1px solid #d0d4d9', borderRadius: '8px', marginRight: '6px' }, attrs: { inputmode: 'numeric' },
        domProps: { value: ns[key] }, on: { input: function (e) { ns[key] = e.target.value; } } }); };
      var timerInputs = [
        h('select', { style: { padding: '6px 8px', borderRadius: '8px', marginRight: '6px', border: '1px solid #d0d4d9' },
          on: { change: function (e) { ns.type = e.target.value; } } },
          [[1, 'Daily'], [0, 'Once'], [2, 'Weekly'], [3, 'Monthly']].map(function (o) {
            return h('option', { attrs: { value: o[0], selected: String(ns.type) === String(o[0]) } }, o[1]); })),
        numInput('hour', '48px'), sub(':', GREY), numInput('minute', '48px'),
        h('select', { style: { padding: '6px 8px', borderRadius: '8px', marginRight: '6px', border: '1px solid #d0d4d9' },
          on: { change: function (e) { ns.action = e.target.value; } } },
          [[1, 'Turn on'], [0, 'Turn off']].map(function (o) {
            return h('option', { attrs: { value: o[0], selected: String(ns.action) === String(o[0]) } }, o[1]); })),
        h('select', { style: { padding: '6px 8px', borderRadius: '8px', marginRight: '6px', border: '1px solid #d0d4d9' },
          on: { change: function (e) { ns.status = e.target.value; } } },
          [[1, 'Enabled'], [-1, 'Disabled']].map(function (o) {
            return h('option', { attrs: { value: o[0], selected: String(ns.status) === String(o[0]) } }, o[1]); })),
        btn('Add', function () { self.addSched(); }, 'primary')
      ];
      var typeValue = Number(ns.type), timerKind = timerInputKind(typeValue);
      if (timerKind === 'date') timerInputs.splice(timerInputs.length - 1, 0,
        h('input', { attrs: { type: 'date', 'aria-label': 'One-shot date' }, domProps: { value: ns.date },
          style: { padding: '6px 8px', border: '1px solid #d0d4d9', borderRadius: '8px', marginRight: '6px' },
          on: { input: function (e) { ns.date = e.target.value; } } }));
      if (timerKind === 'weekly_mask' || timerKind === 'monthly_mask') timerInputs.splice(timerInputs.length - 1, 0,
        h('input', { attrs: { inputmode: 'numeric', 'aria-label': timerKind === 'weekly_mask' ? 'Weekly repeat mask' : 'Monthly repeat mask',
          placeholder: timerKind === 'weekly_mask' ? 'mask 2–254' : 'mask bits 1–31' }, domProps: { value: ns.repeatInput },
          style: { width: '108px', padding: '6px 8px', border: '1px solid #d0d4d9', borderRadius: '8px', marginRight: '6px' },
          on: { input: function (e) { ns.repeatInput = e.target.value.replace(/[^0-9]/g, ''); } } }));
      var addRow = el('div', { display: 'flex', flexWrap: 'wrap', alignItems: 'center', marginTop: '10px', gap: '4px' }, timerInputs);
      var repeatHelp = timerKind === 'weekly_mask' ? 'Weekly mask uses bit 1 Monday through bit 7 Sunday (for example 62 = weekdays).'
        : timerKind === 'monthly_mask' ? 'Monthly mask uses bit 1 for day 1 through bit 31 for day 31.'
          : timerKind === 'date' ? 'One-shot timers require a real calendar date.' : '';
      var schedCard = card([
        cardhead('Schedules'),
        sub('On/off timers stored on the device — they run even if the router is offline.'),
        schRows.length ? el('div', { marginTop: '4px' }, schRows) : el('div', { color: GREY, fontSize: '13px', marginTop: '8px' }, 'No schedules yet.'),
        addRow,
        repeatHelp ? sub(repeatHelp) : ''
      ]);

      // --- Power card ---
      var powerButtons = [btn('Restart', function () { self.confirmAct('restart', 'Restart the Link-Power? It will reconnect in about 15 seconds.'); })];
      if (!self.dev || !self.dev.features || self.dev.features.shutdown) powerButtons.push(
        btn('Power off', function () { self.confirmAct('shutdown', 'Power OFF the Link-Power completely?\n\nThis is a hard shutdown — the device (and anything it powers, including this router if it runs off the battery) will turn off, and it will NOT come back over Bluetooth until you physically power it on again.'); }, 'danger'));
      var powerCard = card([
        cardhead('Power'),
        el('div', { display: 'flex', marginTop: '8px' }, powerButtons)
      ]);

      // --- Router/API administration. API-client enrollment is deliberately
      // separate from the BLE-device pairing card shown while disconnected. ---
      var settingsInfo = self.settings || {};
      var features = self.dev && self.dev.features || {};
      var featureNames = Object.keys(features).filter(function (key) { return features[key] === true; })
        .map(function (key) { return key.replace(/_/g, ' '); }).join(', ') || 'None reported';
      var commands = self.dev && self.dev.commands && self.dev.commands.active || [];
      var kv = function (label, value) { return el('div', { display: 'grid', gridTemplateColumns: '150px minmax(0,1fr)',
        borderTop: '1px solid #e1e5e8', padding: '7px 0', fontSize: '12px' }, [
        el('div', { color: '#6f777d', textTransform: 'uppercase', letterSpacing: '.05em' }, label),
        el('div', { color: '#202124', fontFamily: 'ui-monospace,SFMono-Regular,Consolas,monospace', overflowWrap: 'anywhere' }, value == null || value === '' ? '—' : String(value))
      ]); };
      var identityCard = card([
        cardhead('Device identity'),
        kv('Model', self.dev && self.dev.model), kv('Hardware / variant', self.dev && self.dev.hardware_revision),
        kv('Application firmware', self.dev && self.dev.application_firmware), kv('OTA bootloader', self.dev && self.dev.ota_firmware),
        kv('Device ID / MAC', self.dev && self.dev.id), kv('CID', self.dev && self.dev.cid),
        kv('Capabilities', featureNames), kv('Connection state', self.dev && self.dev.connection && self.dev.connection.phase),
        kv('Pending commands', commands.length ? commands.map(function (c0) { return c0.operation + ' · ' + c0.phase; }).join(', ') : 'None'),
        kv('MagicDNS', self.dev && self.dev.magic_dns_name), kv('TLS certificate SHA-256', settingsInfo.tls && settingsInfo.tls.sha256)
      ]);

      var powerLossModel = self.powerLossClassify(self.powerLossRule ? [self.powerLossRule] : []);
      var powerLossState = self.powerLossDisplay();
      var powerLossText = powerLossState.kind === 'missing' ? 'Preset not configured · no countdown'
        : powerLossState.kind === 'conflict' ? 'Customized rule conflict · no countdown'
          : powerLossState.kind === 'disabled' ? 'Rule disabled · no countdown'
            : powerLossState.kind === 'disconnected' ? 'Disconnected · countdown reset'
              : powerLossState.kind === 'present' ? 'Input power present · countdown reset'
                : powerLossState.kind === 'fired' ? 'Rule last fired ' + new Date(powerLossState.lastFired).toLocaleString() + ' · delivery not confirmed'
                  : powerLossState.kind === 'holding' ? 'Input power absent · ' + Math.ceil(powerLossState.remainingSeconds / 60) + ' min remaining'
                    : 'Input power absent · countdown not active';
      var powerLossDisabled = !!(self.powerLossBusy || self.adminMutations);
      var powerLossButton = function (label, onclick, danger, disabled) {
        return h('button', {
          attrs: { type: 'button', disabled: !!disabled },
          style: { padding: '7px 14px', borderRadius: '8px', fontSize: '13px', marginRight: '8px',
            cursor: disabled ? 'wait' : 'pointer', opacity: disabled ? '.55' : '1', border: danger ? 'none' : '1px solid #d0d4d9',
            background: danger ? RED : '#fff', color: danger ? '#fff' : '#3c4043' },
          on: { click: function () { if (!disabled) onclick(); } }
        }, label);
      };
      var powerLossActions = [powerLossButton('Save', function () { self.savePowerLoss(false); }, false,
        powerLossDisabled || powerLossModel.kind === 'conflict')];
      if (powerLossModel.kind === 'conflict') powerLossActions.push(
        powerLossButton('Reset preset', function () { self.savePowerLoss(true); }, true, powerLossDisabled));
      var powerLossCard = card([
        cardhead('Power-loss shutdown'),
        el('div', { color: RED, fontSize: '12px', margin: '7px 0' },
          'Shutting down Link-Power also powers off this router. It returns only when Link-Power wakes after input power comes back.'),
        el('div', { display: 'flex', flexWrap: 'wrap', alignItems: 'flex-end', gap: '14px', marginTop: '12px' }, [
          h('label', { attrs: { for: 'wl-power-loss-enabled' }, style: { display: 'flex', alignItems: 'center', gap: '7px', fontSize: '13px' } }, [
            h('input', { attrs: { id: 'wl-power-loss-enabled', type: 'checkbox', disabled: powerLossDisabled },
              domProps: { checked: !!self.powerLossDraft.enabled },
              on: { change: function (event) { self.powerLossDraft.enabled = event.target.checked; } } }), 'Enable'
          ]),
          h('label', { attrs: { for: 'wl-power-loss-delay' }, style: { display: 'block', fontSize: '13px' } }, [
            el('div', { color: GREY, fontSize: '12px' }, 'Delay (minutes)'),
            h('input', { attrs: { id: 'wl-power-loss-delay', type: 'number', min: 1, max: 1440, step: 1,
              disabled: powerLossDisabled }, domProps: { value: self.powerLossDraft.minutes },
              style: { width: '96px', padding: '7px 9px', border: '1px solid #cbd1d5', borderRadius: '3px',
                font: '13px ui-monospace,SFMono-Regular,Consolas,monospace' },
              on: { focus: function () { self.powerLossDraft.focused = true; },
                blur: function () { self.powerLossDraft.focused = false; },
                input: function (event) { self.powerLossDraft.minutes = event.target.value; } } })
          ])
        ]),
        h('div', { attrs: { 'aria-live': 'polite' }, style: { color: GREY, fontSize: '13px', marginTop: '10px' } }, powerLossText),
        powerLossModel.kind === 'conflict' ? el('div', { color: RED, fontSize: '12px', marginTop: '7px' },
          'Customized rule conflict. Save is disabled until you reset this preset.') : '',
        self.powerLossError ? el('div', { color: RED, fontSize: '12px', marginTop: '7px' }, self.powerLossError) : '',
        el('div', { display: 'flex', marginTop: '10px' }, powerLossActions)
      ]);

      var apiPairKids = [cardhead('Pair an API client', pill(self.apiPair && self.apiPair.open ? 'Open' : 'Closed',
        self.apiPair && self.apiPair.open ? GREEN : GREY, self.apiPair && self.apiPair.open ? '#e6f6ec' : '#eef1f4')),
        sub('Open a short enrollment window, then scan the authenticated QR in a Wattline client. This is separate from Pair Link-Power over BLE.')];
      if (settingsInfo.pairing_always_on) apiPairKids.push(el('div', { color: RED, fontSize: '12px', padding: '9px 0' },
        'Pairing is always available to anyone with the PIN. Disable always-on pairing for a smaller attack window.'));
      if (self.apiPair && self.apiPair.open) {
        apiPairKids.push(el('div', { textAlign: 'center', margin: '12px 0 4px' }, [sub('Enrollment PIN'),
          el('div', { font: '600 30px ui-monospace,SFMono-Regular,Consolas,monospace', letterSpacing: '.16em' }, self.apiPair.pin || '—'),
          sub(self.apiPair.expires_at ? 'Expires ' + new Date(self.apiPair.expires_at).toLocaleString() : '')]));
        if (self.qrURL) apiPairKids.push(h('img', { attrs: { src: self.qrURL, alt: 'API-client enrollment QR code' },
          style: { display: 'block', width: '180px', height: '180px', margin: '12px auto', border: '1px solid #cbd1d5' } }));
        apiPairKids.push(btn('Close pairing window', function () {
          self.clearQR(); self.adminAction('DELETE', '/pairing-mode', null);
        }, 'danger'));
      } else apiPairKids.push(el('div', { marginTop: '12px' }, [btn('Pair an API client', function () {
        self.adminAction('POST', '/pairing-mode', null);
      }, 'primary')]));
      var apiPairCard = card(apiPairKids);

      var tokenKids = [cardhead('API clients'), sub('Token secrets are shown only once to the client. This inventory contains metadata only.')];
      (self.tokens || []).forEach(function (entry) {
        tokenKids.push(el('div', { display: 'grid', gridTemplateColumns: 'minmax(0,1fr) auto', gap: '8px', alignItems: 'center',
          padding: '10px 0', borderTop: '1px solid #e1e5e8' }, [
          el('div', {}, [el('div', { fontWeight: '600' }, entry.label || entry.id),
            sub('Created ' + (entry.created_at ? new Date(entry.created_at).toLocaleString() : '—') + ' · Last seen ' +
              (entry.last_seen_at ? new Date(entry.last_seen_at).toLocaleString() : 'Never'))]),
          entry.bootstrap ? sub('Administrator') : btn('Revoke', function () {
            self.adminAction('DELETE', '/tokens/' + encodeURIComponent(entry.id), null,
              'Revoke this API client token immediately? The client will be disconnected.');
          }, 'danger')
        ]));
      });
      var tokenCard = card(tokenKids);

      var draft = self.settingsDraft;
      var field = function (label, value, oninput, type) { return el('label', { display: 'block', marginTop: '10px' }, [
        sub(label), h('input', { attrs: { type: type || 'text' }, domProps: { value: value == null ? '' : value },
          style: { boxSizing: 'border-box', width: '100%', padding: '8px 9px', border: '1px solid #cbd1d5', borderRadius: '3px',
            font: '13px ui-monospace,SFMono-Regular,Consolas,monospace', background: '#fbfcfc' },
          on: { input: function (event) { oninput(event.target.value); } } })
      ]); };
      var check = function (label, checked, onchange) { return h('label', { style: { display: 'flex', alignItems: 'center',
        gap: '8px', margin: '9px 0', fontSize: '13px', cursor: 'pointer' } }, [
        h('input', { attrs: { type: 'checkbox' }, domProps: { checked: !!checked }, on: { change: function (event) { onchange(event.target.checked); } } }), label
      ]); };
      var reachKids = [cardhead('Reachability & TLS'),
        sub('VPN interfaces are reachable on enabled listeners. LAN discovery uses mDNS; remote clients use MagicDNS and a saved token.')];
      if (draft) {
        reachKids.push(check('Plain HTTP (use only inside Tailscale/WireGuard)', draft.http.enabled, function (v) { draft.http.enabled = v; }),
          field('HTTP IPv4 bind', draft.http.addr4, function (v) { draft.http.addr4 = v; }),
          field('HTTP IPv6 bind', draft.http.addr6, function (v) { draft.http.addr6 = v; }),
          field('HTTP port', draft.http.port, function (v) { draft.http.port = Number(v); }, 'number'),
          check('HTTPS', draft.https.enabled, function (v) { draft.https.enabled = v; }),
          field('HTTPS IPv4 bind', draft.https.addr4, function (v) { draft.https.addr4 = v; }),
          field('HTTPS IPv6 bind', draft.https.addr6, function (v) { draft.https.addr6 = v; }),
          field('HTTPS port', draft.https.port, function (v) { draft.https.port = Number(v); }, 'number'),
          check('LAN mDNS / DNS-SD', draft.mdns && draft.mdns.enabled, function (v) { draft.mdns.enabled = v; }),
          field('mDNS LAN interfaces (comma-separated)', (draft.mdns && draft.mdns.interfaces || []).join(', '), function (v) {
            draft.mdns.interfaces = v.split(',').map(function (s) { return s.trim(); }).filter(Boolean);
          }),
          check('Allow API from WAN', draft.wan_access, function (v) { draft.wan_access = v; }),
          check('Always allow API-client PIN pairing', draft.pairing_always_on, function (v) { draft.pairing_always_on = v; }),
          field('Pairing window TTL', draft.pairing_ttl, function (v) { draft.pairing_ttl = v; }),
          el('div', { color: draft.wan_access ? RED : GREY, fontSize: '12px', margin: '8px 0' }, draft.wan_access ?
            'WAN access is insecure — use TLS/VPN.' : 'WAN access is off. Tailscale and other VPN interfaces remain reachable.'),
          el('div', { color: ORANGE, fontSize: '12px', margin: '8px 0' },
            'Restart wattlined after listener, TLS, mDNS, or WAN changes. Existing API tokens remain valid.'),
          btn('Save reachability settings', function () { self.saveSettings(); }, 'primary'),
          btn('Rotate TLS certificate', function () { self.adminAction('POST', '/tls/rotate', { confirm: true },
            'Rotate TLS certificate? Saved clients must accept and pin the new fingerprint after wattlined restarts.'); })
        );
      }
      var reachCard = card(reachKids);

      // Both UCI policy and the endpoint's canonical decoded capability must
      // permit each advanced action; one capability never unlocks its siblings.
      var caps = advancedCapabilities(settingsInfo, self.dev);
      var advancedSupported = Object.keys(caps).some(function (key) { return caps[key]; });
      var advancedKids = [cardhead('Advanced controls'),
        check('Enable advanced controls', !!settingsInfo.advanced, function (v) { self.setAdvanced(v); }),
        sub(advancedSupported ? 'Hardware-supported factory operations are unlocked.' :
          (settingsInfo.advanced ? 'No supported advanced operations were reported by this device.' : 'Advanced operations are locked by policy.'))];
      if (advancedSupported) {
        var advancedButtons = [];
        if (caps.ota) advancedButtons.push(btn(self.dev.mode === 'ota' ? 'Exit OTA mode' : 'Enter OTA mode', function () {
          if (self.dev.mode === 'ota') self.adminAction('POST', '/device/ota/exit');
          else self.adminAction('POST', '/device/ota/enter', { confirm: true },
            'Enter OTA mode? Wattline does not flash firmware; use this only for diagnostics.');
        }, self.dev.mode === 'ota' ? null : 'danger'));
        if (caps.clock) advancedButtons.push(btn('Sync device clock', function () {
          self.adminAction('POST', '/device/clock/sync');
        }));
        if (caps.runningMode) advancedButtons.push(btn('Factory running mode', function () {
          var mode = window.prompt('Factory running mode value (device enum):', '1');
          if (mode != null) self.adminAction('PUT', '/device/advanced/running-mode', { mode: Number(mode) }, 'Set Factory running mode to ' + mode + '?');
        }, 'danger'));
        if (caps.barrierFree) advancedButtons.push(btn('Barrier-free mode', function () {
          self.adminAction('GET', '/device/advanced/barrier-free').then(function (state) {
            if (state) self.adminAction('PUT', '/device/advanced/barrier-free', { enabled: !state.enabled },
              (state.enabled ? 'Disable' : 'Enable') + ' barrier-free mode?');
          });
        }));
        if (caps.usbFirmware) advancedButtons.push(btn('Read USB firmware', function () {
          self.adminAction('GET', '/device/advanced/usb-fw-version').then(function (version) {
            if (version) self.usbFirmware = version.major + '.' + version.minor + '.' + version.patch + ' (' + version.raw + ')';
          });
        }));
        if (caps.blePIN) advancedButtons.push(btn('Set BLE PIN', function () {
          var next = window.prompt('New six-digit BLE PIN:');
          if (next != null) self.adminAction('PUT', '/device/advanced/ble-pin', { pin: next }, 'Set BLE PIN? Existing BLE clients may need to pair again.');
        }, 'danger'));
        if (advancedButtons.length) advancedKids.push(el('div', { display: 'flex', flexWrap: 'wrap', gap: '7px', marginTop: '10px' }, advancedButtons));
        if (self.usbFirmware) advancedKids.push(sub('USB firmware ' + self.usbFirmware));
      }
      var advancedCard = card(advancedKids);

      var adminError = self.adminErr ? card([el('div', { color: RED, fontSize: '13px' }, self.adminErr)]) : '';

      var note = el('div', { color: GREY, fontSize: '12px', maxWidth: '460px', margin: '6px 2px 0', lineHeight: '1.5' },
        '~10–15% of the battery is reserved for the Starlink Mini — USB-C output turns off automatically below that to keep your dish running.');

      var adminCards = [identityCard, powerLossCard, apiPairCard, tokenCard, reachCard, advancedCard, adminError];
      if (offlineCards) return wrap(offlineCards.concat(adminCards));
      var deviceCards = [battery];
      var available = self.dev && self.dev.available;
      if (!available || available.dc) deviceCards.push(dcCard);
      if (!available || available.usbc) deviceCards.push(cCard, settings);
      deviceCards.push(schedCard, powerCard, note);
      return wrap(deviceCards.concat(adminCards));
    }
  };
})()
