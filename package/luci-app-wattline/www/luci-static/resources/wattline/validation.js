'use strict';

function ipv4(value) {
	var parts = value.split('.');
	return parts.length === 4 && parts.every(function (part) {
		return /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255;
	});
}

function ipv6(value) {
	if (!value || value.indexOf('%') !== -1 || (value.match(/::/g) || []).length > 1) return false;
	if (value.indexOf('.') !== -1 && value.lastIndexOf('.') < value.lastIndexOf('::')) return false;
	var compressed = value.indexOf('::') !== -1;
	var halves = value.split('::');
	var fields = [];
	halves.forEach(function (half) { if (half) fields = fields.concat(half.split(':')); });
	var count = 0;
	for (var i = 0; i < fields.length; i++) {
		if (fields[i].indexOf('.') !== -1) {
			if (i !== fields.length - 1 || !ipv4(fields[i])) return false;
			count += 2;
		} else {
			if (!/^[0-9A-Fa-f]{1,4}$/.test(fields[i])) return false;
			count++;
		}
	}
	return compressed ? count < 8 : count === 8;
}

function port(value) {
	return /^[0-9]+$/.test(String(value)) && Number(value) >= 1 && Number(value) <= 65535;
}

function positiveDuration(value) {
	var input = String(value || '');
	if (input.charAt(0) === '+') input = input.slice(1);
	var token = /([0-9]+(?:\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)/g;
	var factors = { ns: 1, us: 1e3, 'µs': 1e3, 'μs': 1e3, ms: 1e6, s: 1e9, m: 60e9, h: 3600e9 };
	var offset = 0, nanoseconds = 0, match;
	while ((match = token.exec(input)) !== null) {
		if (match.index !== offset) return false;
		offset = token.lastIndex;
		nanoseconds += Number(match[1]) * factors[match[2]];
	}
	return offset === input.length && offset > 0 && nanoseconds > 0 && isFinite(nanoseconds) && nanoseconds <= 9223372036854775807;
}

function cleanAbsolutePath(value) {
	if (!value || value.charAt(0) !== '/' || /[\0\r\n]/.test(value)) return false;
	if (value.length > 1 && (value.charAt(value.length - 1) === '/' || value.indexOf('//') !== -1)) return false;
	return value.split('/').slice(1).every(function (part) { return part !== '.' && part !== '..'; });
}

var interfaceName = /^[A-Za-z0-9_.:-]{1,15}$/;
function linkLocalV6(value) {
	var first = parseInt(value.split(':')[0], 16);
	return first >= 0xfe80 && first <= 0xfebf;
}
function interfaceSelector(value) {
	if (interfaceName.test(value)) {
		return !(ipv6(value) && linkLocalV6(value));
	}
	var percent = value.lastIndexOf('%');
	if (percent !== -1) {
		var address = value.slice(0, percent), zone = value.slice(percent + 1);
		return ipv6(address) && interfaceName.test(zone);
	}
	return ipv4(value) || ipv6(value);
}

function overlap(config) {
	if (Number(config.httpPort) !== Number(config.httpsPort)) return false;
	var v4 = config.httpAddr4 && config.httpsAddr4 &&
		(config.httpAddr4 === config.httpsAddr4 || config.httpAddr4 === '0.0.0.0' || config.httpsAddr4 === '0.0.0.0');
	var v6 = config.httpAddr6 && config.httpsAddr6 &&
		(config.httpAddr6 === config.httpsAddr6 || config.httpAddr6 === '::' || config.httpsAddr6 === '::');
	return !!(v4 || v6);
}

function validate(config) {
	if (!port(config.httpPort)) return 'HTTP port must be from 1 to 65535';
	if (!port(config.httpsPort)) return 'HTTPS port must be from 1 to 65535';
	for (var i = 0; i < [['HTTP IPv4', config.httpAddr4, ipv4], ['HTTP IPv6', config.httpAddr6, ipv6],
		['HTTPS IPv4', config.httpsAddr4, ipv4], ['HTTPS IPv6', config.httpsAddr6, ipv6]].length; i++) {
		var address = [['HTTP IPv4', config.httpAddr4, ipv4], ['HTTP IPv6', config.httpAddr6, ipv6],
			['HTTPS IPv4', config.httpsAddr4, ipv4], ['HTTPS IPv6', config.httpsAddr6, ipv6]][i];
		if (address[1] && !address[2](address[1])) return address[0] + ' address is invalid';
	}
	if (!config.httpEnabled && !config.httpsEnabled) return 'At least one HTTP or HTTPS listener must be enabled';
	if (config.httpEnabled && !config.httpAddr4 && !config.httpAddr6) return 'Enabled HTTP listener needs an IPv4 or IPv6 address';
	if (config.httpsEnabled && !config.httpsAddr4 && !config.httpsAddr6) return 'Enabled HTTPS listener needs an IPv4 or IPv6 address';
	if (config.httpEnabled && config.httpsEnabled && overlap(config)) return 'HTTP and HTTPS listeners overlap on port ' + config.httpPort;
	for (var p = 0; p < [['TLS certificate', config.tlsCert], ['TLS key', config.tlsKey], ['Token store', config.tokenStore]].length; p++) {
		var path = [['TLS certificate', config.tlsCert], ['TLS key', config.tlsKey], ['Token store', config.tokenStore]][p];
		if (!cleanAbsolutePath(path[1])) return path[0] + ' must be a clean absolute path';
	}
	if (!positiveDuration(config.pairingTTL)) return 'Pairing TTL must be a positive Go duration';
	var interfaces = config.mdnsInterfaces || [], seen = {};
	for (var n = 0; n < interfaces.length; n++) {
		if (!interfaceSelector(interfaces[n])) return 'Invalid mDNS interface ' + interfaces[n];
		if (seen[interfaces[n]]) return 'Duplicate mDNS interface ' + interfaces[n];
		seen[interfaces[n]] = true;
	}
	if (config.mdnsEnabled && interfaces.length === 0) return 'Enabled mDNS needs at least one interface';
	return null;
}

return { validate: validate, ipv4: ipv4, ipv6: ipv6, interfaceSelector: interfaceSelector };
