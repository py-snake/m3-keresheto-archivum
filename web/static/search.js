// Instant search: while typing, the results are replaced by the server-rendered HTML fragment of
// the same search. The page works without this script (a plain GET form); nothing here builds HTML
// from data: the fragment comes from the same origin, escaped by the server's template engine.
(() => {
	'use strict';

	const form = document.getElementById('search-form');
	const results = document.getElementById('results');
	const status = document.getElementById('search-status');
	if (!form || !results || !window.fetch || !window.DOMParser || !window.AbortController) return;

	const DELAY_MS = 300;
	let timer = null;
	let controller = null;

	function queryString() {
		const params = new URLSearchParams();
		for (const [name, value] of new FormData(form)) {
			if (typeof value === 'string' && value.trim() !== '') params.append(name, value);
		}
		return params.toString();
	}

	function say(text) {
		if (status) status.textContent = text;
	}

	let last = queryString();

	async function run() {
		const query = queryString();
		if (query === last) return;
		last = query;

		if (controller) controller.abort();
		controller = new AbortController();
		form.classList.add('searching');
		try {
			const response = await fetch('/?' + (query ? query + '&' : '') + 'reszlet=1', {
				signal: controller.signal,
				headers: { Accept: 'text/html' },
			});
			if (response.status === 429) {
				say('Túl sok kérés, kérlek lassíts.');
				return;
			}
			if (!response.ok && response.status !== 503) {
				say('A keresés nem sikerült.');
				return;
			}
			const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
			results.replaceChildren(...Array.from(doc.body.childNodes));
			history.replaceState(null, '', query ? '/?' + query : '/');
			say('');
		} catch (err) {
			if (err && err.name !== 'AbortError') say('A keresés nem sikerült.');
		} finally {
			form.classList.remove('searching');
		}
	}

	function schedule() {
		clearTimeout(timer);
		timer = setTimeout(run, DELAY_MS);
	}

	form.addEventListener('input', schedule);
	form.addEventListener('change', schedule);
	form.addEventListener('submit', (event) => {
		event.preventDefault();
		clearTimeout(timer);
		run();
	});
})();
