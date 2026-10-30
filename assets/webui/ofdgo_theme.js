(() => {
	const root = document.documentElement;
	const preference = matchMedia("(prefers-color-scheme: dark)");
	const key = "ofdgo.theme";
	let mode = root.dataset.themeMode || "system";
	let host = null, report = null, pending = Promise.resolve();
	if (!root.dataset.themeMode) {
		try { mode = localStorage.getItem(key) || "system"; } catch {}
	}
	if (!["system", "light", "dark"].includes(mode)) mode = "system";
	function apply() {
		root.dataset.themeMode = mode;
		root.dataset.theme = mode === "system" ? preference.matches ? "dark" : "light" : mode;
		document.querySelector('meta[name="theme-color"]').content = root.dataset.theme === "dark" ? "#272e33" : "#fafbfc";
		for (const button of document.querySelectorAll("[data-theme-choice]")) button.dataset.pressed = String(button.dataset.themeChoice === mode);
	}
	function sync() {
		if (!host) return;
		const value = {mode, dark: root.dataset.theme === "dark"};
		pending = pending.then(() => host.call("appearance", value)).catch(error => report(error));
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
	window.ofdgoTheme = {set, connect(value, onError) { host = value; report = onError; apply(); sync(); }};
	apply();
})();
