'use strict';

function create(options) {
	var generation = 0;
	var controller = null;
	var objectURL = null;
	var expiry = null;
	var requestedExpiry = null;
	var pending = null;
	var targetImage = null;

	function detach(image) {
		if (image && image.removeAttribute) image.removeAttribute('src');
	}
	function close(image) {
		generation++;
		if (controller) controller.abort();
		controller = null;
		if (objectURL) options.revokeObjectURL(objectURL);
		objectURL = null;
		expiry = null;
		requestedExpiry = null;
		pending = null;
		detach(image || targetImage);
		targetImage = null;
	}
	function load(image, expiresAt) {
		targetImage = image;
		if (objectURL && expiry === expiresAt) {
			image.src = objectURL;
			return Promise.resolve(objectURL);
		}
		if (pending && requestedExpiry === expiresAt) return pending;
		if (controller) controller.abort();
		var localGeneration = ++generation;
		var localController = new AbortController();
		controller = localController;
		requestedExpiry = expiresAt;
		pending = options.fetchBlob(localController.signal).then(function (blob) {
			if (localGeneration !== generation || localController.signal.aborted) return null;
			var nextURL = options.createObjectURL(blob);
			if (localGeneration !== generation || localController.signal.aborted) {
				options.revokeObjectURL(nextURL);
				return null;
			}
			if (objectURL) options.revokeObjectURL(objectURL);
			objectURL = nextURL;
			expiry = expiresAt;
			controller = null;
			requestedExpiry = null;
			pending = null;
			if (targetImage) targetImage.src = objectURL;
			return objectURL;
		}, function (error) {
			if (localGeneration !== generation || localController.signal.aborted || (error && error.name === 'AbortError')) return null;
			controller = null;
			requestedExpiry = null;
			pending = null;
			throw error;
		});
		return pending;
	}

	return { load: load, close: close };
}

return { create: create };
