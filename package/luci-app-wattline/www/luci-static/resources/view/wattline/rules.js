'use strict';
'require view';
'require form';
'require uci';

return view.extend({
	load: function () { return uci.load('wattline'); },
	render: function () {
		var m = new form.Map('wattline', _('Wattline Rules'),
			_('Automation rules. Blank hold fires immediately; shutdown requires the confirm flag.'));
		var s = m.section(form.GridSection, 'rule', _('Rules'));
		s.addremove = true;
		s.anonymous = false;
		s.nodescriptions = true;

		s.option(form.Flag, 'enabled', _('On'));
		var cond = s.option(form.ListValue, 'condition', _('Condition'));
		cond.value('input_power', _('Input power'));
		cond.value('battery_level', _('Battery level'));
		cond.value('port_power', _('Port power'));
		cond.value('schedule', _('Schedule'));

		var st = s.option(form.ListValue, 'state', _('Input'));
		st.value('absent', _('absent')); st.value('present', _('present'));
		st.depends('condition', 'input_power');

		var op = s.option(form.ListValue, 'op', _('Op'));
		op.value('below', _('below')); op.value('above', _('above'));
		op.depends('condition', 'battery_level');
		op.depends('condition', 'port_power');

		var pct = s.option(form.Value, 'percent', _('%'));
		pct.datatype = 'range(0,100)';
		pct.depends('condition', 'battery_level');

		var port = s.option(form.ListValue, 'port', _('Port'));
		port.value('dc', 'DC'); port.value('usbc', 'USB-C');
		port.depends('condition', 'port_power');
		s.option(form.Value, 'watts', _('Watts')).depends('condition', 'port_power');

		s.option(form.Value, 'cron', _('Cron')).depends('condition', 'schedule');
		s.option(form.Value, 'hold', _('Hold'), _('e.g. 10m'));
		s.option(form.DynamicList, 'action', _('Actions'),
			_('dc_off, dc_on, usbc_off, usbc_on, bypass_off, bypass_on, restart, shutdown, webhook:<url>'));
		s.option(form.Flag, 'confirm_shutdown', _('Confirm shutdown'),
			_('Required to allow a shutdown action. Device wakes on button press or when PD power is plugged in.'));
		return m.render();
	}
});
