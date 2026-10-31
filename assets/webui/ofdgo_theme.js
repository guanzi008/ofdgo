(() => {
	const root = document.documentElement;
	const preference = matchMedia("(prefers-color-scheme: dark)");
	const key = "ofdgo.theme";
	let mode = root.dataset.themeMode || "system";
	let material = root.dataset.windowChoice || "solid";
	let host = null, report = null, pending = Promise.resolve(), revision = 0, committed = null;
	if (!root.dataset.themeMode) {
		try { mode = localStorage.getItem(key) || "system"; } catch {}
	}
	if (!["system", "light", "dark"].includes(mode)) mode = "system";
	function apply() {
		root.dataset.themeMode = mode;
		root.dataset.theme = mode === "system" ? preference.matches ? "dark" : "light" : mode;
		document.querySelector('meta[name="theme-color"]').content = root.dataset.theme === "dark" ? "#272e33" : "#fafbfc";
		for (const button of document.querySelectorAll("[data-theme-choice]")) button.dataset.pressed = String(button.dataset.themeChoice === mode);
		for (const button of document.querySelectorAll("[data-material-choice]")) button.dataset.pressed = String(button.dataset.materialChoice === material);
	}
	function sync() {
		if (!host) return;
		const value = {mode, dark: root.dataset.theme === "dark", material};
		const current = ++revision;
		pending = pending.then(() => host.call("appearance", value)).then(() => { committed = value; }).catch(error => {
			if (current === revision) {
				mode = committed.mode;
				material = committed.material;
				apply();
			}
			report(error);
		});
	}
	async function flush() {
		let current;
		do {
			current = pending;
			await current;
		} while (current !== pending);
	}
	function set(value) {
		mode = value;
		apply();
		if (!host) {
			try { localStorage.setItem(key, mode); } catch {}
		}
		sync();
	}
	preference.addEventListener("change", () => {
		if (mode !== "system") return;
		apply();
		sync();
	});
	window.addEventListener("storage", event => {
		if (host || event.key !== key && event.key !== null) return;
		mode = ["light", "dark"].includes(event.newValue) ? event.newValue : "system";
		apply();
	});
	window.ofdgoTheme = {set, flush, connect(value, onError) {
		host = value;
		report = onError;
		const materials = (root.dataset.windowMaterials || "").split(",");
		if (host && materials.length > 1) {
			document.querySelector(".appearance-material").hidden = false;
			for (const button of document.querySelectorAll("[data-material-choice]")) {
				button.hidden = !materials.includes(button.dataset.materialChoice);
				button.addEventListener("click", () => { material = button.dataset.materialChoice; apply(); sync(); });
			}
		}
		apply();
		sync();
	}};
	apply();
	committed = {mode, material};
})();
