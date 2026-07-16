'use strict';
'require view';
'require form';
'require uci';

return view.extend({
	load: function () { return uci.load('wattline'); },
	render: function () {
		var m = new form.Map('wattline', _('Wattline Settings'),
			_('Connection and API settings for the Link-Power automation daemon.'));
		var s = m.section(form.NamedSection, 'main', 'wattline', _('Daemon'));
		s.option(form.Value, 'device_mac', _('Device MAC'),
			_('Leave blank to auto-pick the first Link-Power found.'));
		s.option(form.Value, 'pin', _('Pairing PIN'),
			_('Default fixed PIN is 020555 (see the device manual).'));
		var port = s.option(form.Value, 'port', _('API Port'));
		port.datatype = 'port';
		s.option(form.Flag, 'lan_api', _('Expose API on LAN'),
			_('Off = localhost only.'));
		s.option(form.Value, 'token', _('API Token'),
			_('Bearer token for the HTTP API. Auto-generated on install.'));
		return m.render();
	}
});
