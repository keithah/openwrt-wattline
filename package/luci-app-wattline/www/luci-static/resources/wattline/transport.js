'use strict';

function bases(config, host) {
	var values = [];
	if (config.httpsEnabled) values.push('https://' + host + ':' + config.httpsPort + '/api/v1');
	if (config.httpEnabled) values.push('http://' + host + ':' + config.httpPort + '/api/v1');
	return values;
}

function responseError(response) {
	return response.json().then(function (payload) {
		var detail = payload && payload.error;
		var error = new Error(detail && detail.message ? detail.message : ('HTTP ' + response.status));
		error.code = detail && detail.code;
		error.status = response.status;
		throw error;
	}).catch(function (error) {
		if (error && error.status) throw error;
		var fallback = new Error('HTTP ' + response.status);
		fallback.status = response.status;
		throw fallback;
	});
}

function create(options) {
	var endpoints = bases(options.config, options.host);
	var selected = null;
	var fetchImpl = options.fetch;

	function response(method, path, body, extra) {
		method = method.toUpperCase();
		var safe = method === 'GET';
		var candidates;
		if (safe) {
			candidates = selected ? [selected].concat(endpoints.filter(function (base) { return base !== selected; })) : endpoints.slice();
		} else {
			// A connection failure after a write is ambiguous: the daemon may have
			// applied it. Pin mutations to one endpoint and require explicit retry.
			candidates = [selected || endpoints[0]].filter(Boolean);
		}
		var index = 0;
		function attempt(lastError) {
			if (index >= candidates.length) return Promise.reject(lastError || new Error('No API listener is enabled'));
			var base = candidates[index++];
			var headers = { 'Authorization': 'Bearer ' + options.token };
			if (body != null) headers['Content-Type'] = 'application/json';
			var request = {
				method: method, headers: headers, body: body == null ? null : JSON.stringify(body), cache: 'no-store'
			};
			if (extra && extra.signal) request.signal = extra.signal;
			return fetchImpl(base + path, request).then(function (result) {
				selected = base;
				return result;
			}, function (error) {
				var aborted = (extra && extra.signal && extra.signal.aborted) || (error && error.name === 'AbortError');
				if (safe && !aborted && index < candidates.length) return attempt(error);
				throw error;
			});
		}
		return attempt();
	}

	function checked(method, path, body, extra) {
		return response(method, path, body, extra).then(function (result) {
			if (!result.ok) return responseError(result);
			return result;
		});
	}

	return {
		response: response,
		json: function (method, path, body, extra) {
			return checked(method, path, body, extra).then(function (result) { return result.json(); });
		},
		blob: function (method, path, body, extra) {
			return checked(method, path, body, extra).then(function (result) { return result.blob(); });
		}
	};
}

return { create: create };
