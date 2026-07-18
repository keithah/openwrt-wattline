'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require wattline.validation as wattlineValidation';

function confirmFlag(option, message) {
	option.onchange = function (event, sectionID, value) {
		if (String(value) === '1' && !window.confirm(message)) {
			option.getUIElement(sectionID).setValue('0');
		}
	};
}

return view.extend({
	load: function () { return uci.load('wattline'); },
	render: function () {
		var map = new form.Map('wattline', _('Wattline Settings'),
			_('Listener, discovery, and security settings for wattlined. Listener and certificate changes take effect after a daemon restart.'));
		var main = map.section(form.NamedSection, 'main', 'wattline', _('Link-Power over BLE'));
		main.anonymous = true;
		main.option(form.Value, 'device_mac', _('Device MAC'), _('Leave blank to discover the first Link-Power.'));
		var blePIN = main.option(form.Value, 'pin', _('BLE pairing PIN'), _('Used only to Pair Link-Power over BLE; this is not the API-client enrollment PIN.'));
		blePIN.datatype = 'and(uinteger,minlength(6),maxlength(6))';
		blePIN.password = true;

		var listeners = map.section(form.NamedSection, 'main', 'wattline', _('API listeners'));
		listeners.anonymous = true;
		var httpEnabled = listeners.option(form.Flag, 'http_enabled', _('Plain HTTP'), _('Useful inside Tailscale/WireGuard. Prefer HTTPS on untrusted networks.'));
		httpEnabled.default = '1';
		var http4 = listeners.option(form.Value, 'http_addr4', _('HTTP IPv4 bind'));
		http4.default = '0.0.0.0'; http4.datatype = 'or(empty,ip4addr)';
		var http6 = listeners.option(form.Value, 'http_addr6', _('HTTP IPv6 bind'));
		http6.default = '::'; http6.datatype = 'or(empty,ip6addr)';
		var httpPort = listeners.option(form.Value, 'port', _('HTTP port'));
		httpPort.datatype = 'port'; httpPort.default = '8377';
		var httpsEnabled = listeners.option(form.Flag, 'https_enabled', _('HTTPS'));
		httpsEnabled.default = '1';
		var https4 = listeners.option(form.Value, 'https_addr4', _('HTTPS IPv4 bind'));
		https4.default = '0.0.0.0'; https4.datatype = 'or(empty,ip4addr)';
		var https6 = listeners.option(form.Value, 'https_addr6', _('HTTPS IPv6 bind'));
		https6.default = '::'; https6.datatype = 'or(empty,ip6addr)';
		var httpsPort = listeners.option(form.Value, 'https_port', _('HTTPS port'));
		httpsPort.datatype = 'port'; httpsPort.default = '8378';
		var tlsCert = listeners.option(form.Value, 'tls_cert', _('TLS certificate path'));
		var tlsKey = listeners.option(form.Value, 'tls_key', _('TLS private-key path'));
		var tokenStore = listeners.option(form.Value, 'token_store', _('Managed-token store path'),
			_('Metadata and SHA-256 token hashes only; the file is maintained with mode 0600.'));
		http4.depends('http_enabled', '1'); http6.depends('http_enabled', '1'); httpPort.depends('http_enabled', '1');
		https4.depends('https_enabled', '1'); https6.depends('https_enabled', '1'); httpsPort.depends('https_enabled', '1');
		tlsCert.depends('https_enabled', '1'); tlsKey.depends('https_enabled', '1');
		http4.retain = http6.retain = httpPort.retain = true;
		https4.retain = https6.retain = httpsPort.retain = tlsCert.retain = tlsKey.retain = true;

		var enrollment = map.section(form.NamedSection, 'main', 'wattline', _('API-client enrollment'));
		enrollment.anonymous = true;
		var ttl = enrollment.option(form.Value, 'pairing_ttl', _('Pairing window TTL'), _('Duration such as 5m or 90s.'));
		ttl.default = '5m';
		var always = enrollment.option(form.Flag, 'pairing_always_on', _('Always allow PIN pairing'),
			_('Less secure: enrollment is always available to anyone with the PIN. Prefer opening a short pairing window from Status.'));
		always.default = '0';
		confirmFlag(always, _('Make API-client pairing always available to anyone with the PIN?'));

		var discovery = map.section(form.NamedSection, 'main', 'wattline', _('Discovery and reachability'));
		discovery.anonymous = true;
		var mdns = discovery.option(form.Flag, 'mdns_enabled', _('LAN mDNS / DNS-SD'), _('Advertise _wattline._tcp only on explicitly selected LAN interfaces.'));
		mdns.default = '1';
		var interfaces = discovery.option(form.DynamicList, 'mdns_interface', _('mDNS LAN interfaces'));
		interfaces.placeholder = 'br-lan';
		interfaces.depends('mdns_enabled', '1');
		interfaces.retain = true;
		var wan = discovery.option(form.Flag, 'wan_access', _('Allow API from WAN'),
			_('Insecure — use TLS/VPN. This opens enabled API ports on the WAN firewall zone.'));
		wan.default = '0';
		confirmFlag(wan, _('Enable WAN access? This is insecure — use TLS/VPN and strong client tokens.'));

		var advanced = map.section(form.NamedSection, 'main', 'wattline', _('Advanced device controls'));
		advanced.anonymous = true;
		var advancedFlag = advanced.option(form.Flag, 'advanced', _('Enable advanced controls'),
			_('Exposes factory running mode, barrier-free mode, USB firmware, and BLE-PIN controls.'));
		advancedFlag.default = '0';

		function enabled(option) {
			var value = option.formvalue('main');
			if (value == null) value = uci.get('wattline', 'main', option.option);
			return value === true || String(value) === '1';
		}
		function field(option, fallback) {
			var value = option.formvalue('main');
			if (value == null) value = uci.get('wattline', 'main', option.option);
			return value == null ? fallback : value;
		}
		function current(overriddenKey, overriddenValue) {
			var list = field(interfaces, []);
			if (list == null || list === '') list = [];
			if (!Array.isArray(list)) list = [list];
			var values = {
				httpEnabled: enabled(httpEnabled), httpAddr4: field(http4, ''),
				httpAddr6: field(http6, ''), httpPort: field(httpPort, ''),
				httpsEnabled: enabled(httpsEnabled), httpsAddr4: field(https4, ''),
				httpsAddr6: field(https6, ''), httpsPort: field(httpsPort, ''),
				tlsCert: field(tlsCert, ''), tlsKey: field(tlsKey, ''),
				tokenStore: field(tokenStore, ''), pairingTTL: field(ttl, ''),
				mdnsEnabled: enabled(mdns), mdnsInterfaces: list
			};
			values[overriddenKey] = overriddenValue;
			return values;
		}
		function validateSetting(key, normalize) {
			return function (sectionID, value) {
				var error = wattlineValidation.validate(current(key, normalize ? normalize(value) : value));
				return error || true;
			};
		}
		var flagValue = function (value) { return value === true || String(value) === '1'; };
		httpEnabled.validate = validateSetting('httpEnabled', flagValue);
		httpsEnabled.validate = validateSetting('httpsEnabled', flagValue);
		http4.validate = validateSetting('httpAddr4'); http6.validate = validateSetting('httpAddr6');
		https4.validate = validateSetting('httpsAddr4'); https6.validate = validateSetting('httpsAddr6');
		httpPort.validate = validateSetting('httpPort'); httpsPort.validate = validateSetting('httpsPort');
		tlsCert.validate = validateSetting('tlsCert'); tlsKey.validate = validateSetting('tlsKey');
		tokenStore.validate = validateSetting('tokenStore'); ttl.validate = validateSetting('pairingTTL');
		mdns.validate = validateSetting('mdnsEnabled', flagValue);
		interfaces.validate = validateSetting('mdnsInterfaces', function (value) {
			if (value == null || value === '') return [];
			return Array.isArray(value) ? value : [value];
		});

		return map.render().then(function (node) {
			node.insertBefore(E('div', { 'class': 'alert-message warning' },
				_('Restart wattlined after changing listener, TLS, mDNS, or WAN settings. Existing API tokens remain valid.')), node.firstChild);
			return node;
		});
	}
});
