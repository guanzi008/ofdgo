export class DocumentDialogs {
	constructor({ document: root = document, targets = [], titlebar = null } = {}) {
		Object.assign(this, { document: root, targets, titlebar, owner: null, records: new Map(), tabs: new Map(), released: new WeakSet(), inert: new Map(), styles: new Map() });
		this.backdrop = root.createElement("div");
		this.backdrop.className = "document-dialog-backdrop";
		this.backdrop.hidden = true;
		Object.assign(this.backdrop.style, { position: "fixed", inset: "0", background: "rgb(31 36 40 / 18%)", zIndex: "1000" });
		root.body.append(this.backdrop);
		root.addEventListener("keydown", event => {
			if (event.defaultPrevented || this.applicationOpen()) return;
			const panel = this.current()?.stack.at(-1);
			if (!panel) return;
			if (event.key === "Tab" && !event.ctrlKey && !event.altKey && !event.metaKey) {
				const selector = "button, input, select, textarea, a[href], [tabindex], [contenteditable=true]";
				const elements = [this.titlebar, panel].filter(Boolean).flatMap(root => [...root.querySelectorAll(selector)])
					.filter(node => node.tabIndex >= 0 && !node.disabled && !node.closest("[inert]") && node.getClientRects().length);
				const index = elements.indexOf(root.activeElement), direction = event.shiftKey ? -1 : 1;
				const target = index < 0 ? event.shiftKey ? elements.at(-1) : elements[0] : elements[(index + direction + elements.length) % elements.length];
				event.preventDefault();
				if (target) target.focus();
				else panel.focus();
				return;
			}
			if (event.key !== "Escape") return;
			event.preventDefault();
			event.stopPropagation();
			this.cancel(panel);
		}, true);
		this.backdrop.addEventListener("pointerdown", event => { event.preventDefault(); this.focus(); });
		root.defaultView.addEventListener("resize", () => this.paint());
	}

	register(panel, { shared = true, persistent = false } = {}) {
		if (this.records.has(panel)) return panel;
		const record = { show: panel.show.bind(panel), showModal: panel.showModal.bind(panel), close: panel.close.bind(panel), owner: null, application: false, shared, clicks: new WeakMap(), initial: shared && !persistent ? this.snapshot(panel) : null };
		this.records.set(panel, record);
		panel.showModal = panel.show = () => this.openFor(this.owner, panel);
		panel.close = value => {
			if (record.application) record.close(value);
			else this.closeFor(this.owner, panel, value);
		};
		panel.requestClose = value => {
			if (record.application) {
				const event = new this.document.defaultView.Event("cancel", { cancelable: true });
				if (panel.dispatchEvent(event)) record.close(value);
			} else this.cancel(panel, value);
		};
		panel.addEventListener("close", () => {
			if (record.application) { record.application = false; this.paint(); }
		});
		panel.addEventListener("click", event => { record.clicks.set(event, this.owner); }, true);
		panel.addEventListener("click", event => {
			if (!record.application && (record.clicks.get(event) !== this.owner || !this.current()?.stack.includes(panel))) event.preventDefault();
		});
		panel.addEventListener("submit", event => {
			if (record.application || event.defaultPrevented) return;
			const method = event.submitter?.getAttribute("formmethod") || event.target.getAttribute("method");
			if (method?.toLowerCase() !== "dialog") return;
			event.preventDefault();
			this.closeFor(this.owner, panel, event.submitter?.value || "");
		});
		return panel;
	}

	current(tab = this.owner) {
		return tab ? this.tabs.get(tab) : null;
	}

	entry(tab, panel) {
		if (!tab || this.released.has(tab)) throw new Error("Document dialog owner is unavailable");
		this.register(panel);
		if (!this.tabs.has(tab)) this.tabs.set(tab, { entries: new Map(), stack: [] });
		const entries = this.tabs.get(tab).entries;
		if (!entries.has(panel)) entries.set(panel, { snapshot: this.records.get(panel).initial, setup: [], updates: [], resolve: null, promise: null });
		return entries.get(panel);
	}

	setOwner(tab) {
		if (this.owner === tab) return;
		if (this.owner) this.suspend(this.owner);
		if (tab) this.restore(tab);
	}

	openFor(tab, panel, setup) {
		const entry = this.entry(tab, panel);
		if (setup) entry.setup.push(setup);
		const state = this.tabs.get(tab);
		state.stack = state.stack.filter(value => value !== panel);
		state.stack.push(panel);
		if (tab !== this.owner) return panel;
		this.unpaint();
		this.records.get(panel).owner = tab;
		this.prepare(entry, panel);
		if (!this.records.get(panel).shared) this.document.body.append(panel);
		if (!panel.open) this.records.get(panel).show();
		this.paint();
		this.focus();
		return panel;
	}

	request(panel, tab = this.owner, setup) {
		if (!tab || this.released.has(tab)) return Promise.resolve(null);
		const entry = this.entry(tab, panel);
		if (entry.promise) return entry.promise;
		entry.promise = new Promise(resolve => { entry.resolve = resolve; });
		const promise = entry.promise;
		this.openFor(tab, panel, setup);
		return promise;
	}

	updateFor(tab, panel, setup) {
		if (!tab || this.released.has(tab)) return false;
		const entry = this.current(tab)?.entries.get(panel);
		if (!entry) return false;
		if (tab === this.owner && this.records.get(panel).owner === tab) setup(panel);
		else entry.updates.push(setup);
		return true;
	}

	closeFor(tab, panel, value) {
		const state = this.current(tab), entry = state?.entries.get(panel);
		if (!state?.stack.includes(panel)) return;
		const visible = tab === this.owner;
		if (visible) {
			this.unpaint();
			if (value !== undefined) panel.returnValue = String(value);
			panel.removeAttribute("open");
			if (this.records.get(panel).initial) entry.snapshot = this.snapshot(panel);
		}
		const result = value === undefined ? visible ? panel.returnValue : entry.snapshot?.returnValue ?? panel.returnValue : String(value);
		state.stack = state.stack.filter(value => value !== panel);
		const resolve = entry.resolve;
		entry.resolve = entry.promise = null;
		entry.setup = [];
		if (visible) {
			this.paint();
			panel.dispatchEvent(new this.document.defaultView.Event("close"));
			this.focus();
		}
		resolve?.(result);
	}

	cancel(panel, value) {
		const event = new this.document.defaultView.Event("cancel", { cancelable: true });
		if (panel.dispatchEvent(event)) this.closeFor(this.owner, panel, value);
	}

	suspend(tab) {
		if (tab !== this.owner) return;
		this.unpaint();
		for (const [panel, entry] of this.current(tab)?.entries || []) {
			const record = this.records.get(panel);
			if (record.application || record.owner !== tab) continue;
			if (record.initial) entry.snapshot = this.snapshot(panel);
			else if (!record.shared) {
				entry.focus = panel.contains(this.document.activeElement) ? this.document.activeElement : null;
				panel.remove();
			}
			panel.removeAttribute("open");
		}
		this.owner = null;
		this.paint();
	}

	restore(tab) {
		if (!tab || this.released.has(tab)) return false;
		if (this.owner === tab) return true;
		if (this.owner) this.suspend(this.owner);
		if (!this.tabs.has(tab)) this.tabs.set(tab, { entries: new Map(), stack: [] });
		this.owner = tab;
		const state = this.current(tab);
		for (const [panel, entry] of state.entries) {
			const record = this.records.get(panel);
			if (record.application) continue;
			if (record.initial) this.applySnapshot(entry.snapshot, panel);
			else if (!record.shared) this.document.body.append(panel);
			record.owner = tab;
		}
		for (const [panel, entry] of state.entries) {
			if (this.records.get(panel).application) continue;
			for (const update of entry.updates.splice(0)) update(panel);
		}
		for (const panel of state.stack) {
			this.prepare(state.entries.get(panel), panel);
			if (!panel.open) this.records.get(panel).show();
		}
		this.paint();
		this.focus(true);
		return true;
	}

	release(tab) {
		if (!tab || this.released.has(tab)) return;
		const visible = this.owner === tab;
		if (visible) this.suspend(tab);
		const state = this.current(tab);
		this.tabs.delete(tab);
		this.released.add(tab);
		for (const entry of state?.entries.values() || []) entry.resolve?.(null);
		for (const [panel] of state?.entries || []) {
			const record = this.records.get(panel);
			if (!record.shared) {
				panel.remove();
				this.records.delete(panel);
				continue;
			}
			if (record.application || record.owner !== tab) continue;
			if (record.initial) this.applySnapshot(record.initial, panel);
			record.owner = null;
		}
		state?.entries.clear();
		if (state) state.stack.length = 0;
	}

	showApplication(panel) {
		this.register(panel);
		if ([...this.tabs.values()].some(state => state.stack.includes(panel))) throw new Error("Document dialog is already in use");
		const record = this.records.get(panel);
		record.application = true;
		try { record.showModal(); }
		catch (error) { record.application = false; throw error; }
	}

	applicationOpen() {
		return [...this.records.values()].some(record => record.application);
	}

	prepare(entry, panel) {
		for (const setup of entry.setup.splice(0)) setup(panel);
	}

	snapshot(panel) {
		const nodes = [panel, ...panel.querySelectorAll("*")].map(node => ({ node,
			children: [...node.childNodes], attributes: [...node.attributes].filter(attribute => node !== panel || attribute.name !== "open").map(attribute => [attribute.name, attribute.value]),
			value: "value" in node && node.type !== "file" ? node.value : undefined,
			checked: node.checked, indeterminate: node.indeterminate, selected: node.selected,
			files: node.type === "file" ? [...node.files] : undefined, scrollTop: node.scrollTop, scrollLeft: node.scrollLeft,
			selectionStart: node.selectionStart, selectionEnd: node.selectionEnd, selectionDirection: node.selectionDirection,
			text: [...node.childNodes].filter(child => child.nodeType === 3).map(child => [child, child.data]) }));
		const focus = panel.contains(this.document.activeElement) ? this.document.activeElement : null;
		const range = this.document.getSelection();
		const selection = !focus?.matches("input, textarea") && range?.anchorNode && panel.contains(range.anchorNode) && panel.contains(range.focusNode)
			? { anchor: range.anchorNode, start: range.anchorOffset, focus: range.focusNode, end: range.focusOffset } : null;
		return { nodes, focus, selection, returnValue: panel.returnValue };
	}

	applySnapshot(snapshot, panel) {
		panel.removeAttribute("open");
		for (const item of snapshot.nodes) {
			const { node, children, attributes } = item;
			if (node.childNodes.length !== children.length || children.some((child, index) => node.childNodes[index] !== child)) node.replaceChildren(...children);
			for (const attribute of [...node.attributes]) if (!attributes.some(([name]) => name === attribute.name)) node.removeAttribute(attribute.name);
			for (const [name, value] of attributes) if (node.getAttribute(name) !== value) node.setAttribute(name, value);
			for (const [text, value] of item.text) text.data = value;
		}
		for (const item of snapshot.nodes) {
			const { node } = item;
			if (item.value !== undefined && node.tagName !== "SELECT") node.value = item.value;
			for (const key of ["checked", "indeterminate", "selected"]) if (item[key] !== undefined) node[key] = item[key];
			if (item.files !== undefined) {
				const transfer = new this.document.defaultView.DataTransfer();
				for (const file of item.files) transfer.items.add(file);
				node.files = transfer.files;
			}
			if (item.selectionStart != null && typeof node.setSelectionRange === "function") node.setSelectionRange(item.selectionStart, item.selectionEnd, item.selectionDirection);
			node.scrollTop = item.scrollTop;
			node.scrollLeft = item.scrollLeft;
		}
		panel.returnValue = snapshot.returnValue;
	}

	unpaint() {
		for (const [node, value] of this.inert) node.inert = value;
		this.inert.clear();
		for (const [panel, styles] of this.styles) for (const [name, [value, priority]] of styles) {
			if (value) panel.style.setProperty(name, value, priority);
			else panel.style.removeProperty(name);
		}
		this.styles.clear();
	}

	paint() {
		this.unpaint();
		const stack = this.current()?.stack || [];
		const top = stack.at(-1);
		this.backdrop.hidden = !top;
		if (!top) return;
		const inset = Math.max(0, this.titlebar?.getBoundingClientRect().bottom || 0);
		this.backdrop.style.top = `${inset}px`;
		this.backdrop.style.zIndex = String(1000 + (stack.length - 1) * 2);
		const disable = node => {
			if (!node || node === this.titlebar || node === this.backdrop || this.records.has(node)) return;
			if (node.contains(this.titlebar) || [...this.records.keys()].some(panel => node.contains(panel))) {
				for (const child of node.children) disable(child);
				return;
			}
			if (!this.inert.has(node)) this.inert.set(node, node.inert);
			node.inert = true;
		};
		for (const target of typeof this.targets === "function" ? this.targets() : this.targets) disable(target);
		for (const [index, panel] of stack.entries()) {
			if (panel !== top) { this.inert.set(panel, panel.inert); panel.inert = true; }
			const values = { position: "fixed", inset: `${inset}px 0 0`, zIndex: String(1001 + index * 2), maxHeight: `calc(100dvh - ${inset + 24}px)`, "--dialog-inset": `${inset}px` };
			const styles = new Map();
			for (const [key, value] of Object.entries(values)) {
				const name = key.replace(/[A-Z]/g, letter => `-${letter.toLowerCase()}`);
				styles.set(name, [panel.style.getPropertyValue(name), panel.style.getPropertyPriority(name)]);
				panel.style.setProperty(name, value);
			}
			this.styles.set(panel, styles);
		}
	}

	focus(restore = false) {
		const panel = this.current()?.stack.at(-1);
		if (!panel || this.applicationOpen()) return;
		const saved = restore ? this.current().entries.get(panel)?.snapshot?.focus || this.current().entries.get(panel)?.focus : null;
		const target = saved?.isConnected && panel.contains(saved) ? saved : panel.querySelector("[autofocus], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]") || panel;
		if (restore || !panel.contains(this.document.activeElement)) target.focus({ preventScroll: true });
		const selection = restore && this.current().entries.get(panel)?.snapshot?.selection;
		if (selection?.anchor?.isConnected && selection.focus?.isConnected) this.document.getSelection().setBaseAndExtent(selection.anchor, selection.start, selection.focus, selection.end);
		if (restore) for (const item of this.current().entries.get(panel)?.snapshot?.nodes || []) { item.node.scrollTop = item.scrollTop; item.node.scrollLeft = item.scrollLeft; }
	}
}
