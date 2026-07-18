'use strict';

function create(options) {
	var generation = 0;
	var pendingMutations = 0;

	function refresh() {
		if (pendingMutations) return Promise.resolve(null);
		var localGeneration = ++generation;
		return options.load().then(function (value) {
			if (pendingMutations || localGeneration !== generation) return null;
			options.render(value);
			return value;
		}, function (error) {
			if (pendingMutations || localGeneration !== generation) return null;
			if (options.error) options.error(error);
			else throw error;
			return null;
		});
	}

	function finish(value, failed) {
		pendingMutations--;
		if (pendingMutations) return failed ? Promise.reject(value) : Promise.resolve(value);
		return refresh().then(function () {
			if (failed) throw value;
			return value;
		}, function (refreshError) {
			if (failed) throw value;
			throw refreshError;
		});
	}

	function mutation(work) {
		pendingMutations++;
		generation++;
		return Promise.resolve().then(work).then(function (value) {
			return finish(value, false);
		}, function (error) {
			return finish(error, true);
		});
	}

	return { refresh: refresh, mutation: mutation };
}

return { create: create };
