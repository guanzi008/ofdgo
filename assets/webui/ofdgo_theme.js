(() => {
	const root = document.documentElement;
	const preference = matchMedia("(prefers-color-scheme: dark)");
	const key = "ofdgo.theme";
	let mode = root.dataset.themeMode || "system";
	let material = root.dataset.titlebarChoice || "mica";
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
		const select = document.querySelector("#titleBarMaterial");
		if (select) select.value = material;
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
	window.ofdgoTheme = {set, connect(value, onError) {
		host = value;
		report = onError;
		const select = document.querySelector("#titleBarMaterial");
		const materials = (root.dataset.titlebarMaterials || "").split(",");
		if (host && materials.length > 1) {
			for (const option of [...select.options]) if (!materials.includes(option.value)) option.remove();
			select.closest(".appearance-material").hidden = false;
			select.addEventListener("change", () => { material = select.value; sync(); });
		}
		apply();
		sync();
	}};
	apply();
	committed = {mode, material};
})();
