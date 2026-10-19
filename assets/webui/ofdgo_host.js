let engineModule = null;
let engineScheduler = null;

export function engineLimits() {
	engineScheduler ||= new BrowserScheduler();
	return {parallelism: engineScheduler.capacity, memoryBytes: engineScheduler.budget};
}

class BrowserScheduler {
	constructor() {
		this.budget = Math.max(256, Math.min(2048, (navigator.deviceMemory || 2) * 256)) * 1024 * 1024;
		this.capacity = Math.max(1, Math.min((navigator.hardwareConcurrency || 2) - 1, Math.floor(this.budget / (128 << 20))));
		this.limit = Math.min(this.capacity, Math.max(1, Math.floor(this.budget / (512 << 20))));
		this.workers = new Set();
		this.queue = [];
	}

	get memory() {
		return [...this.workers].reduce((total, worker) => total + Math.max(worker.memory, worker.reservation), 0);
	}

	active() {
		return [...this.workers].filter(worker => worker.requests.size).length;
	}

	submit(worker, data, transfer) {
		if (worker.primary) { this.send({worker, data, transfer}); return; }
		this.queue.push({worker, data, transfer, queued:performance.now()});
		this.drain();
	}

	send(task) {
		const {worker, data, transfer} = task;
		worker.requests.set(data.id, performance.now());
		try { worker.worker.postMessage(data, transfer); }
		catch (error) {
			worker.requests.delete(data.id);
			worker.onmessage?.({data:{id:data.id, ok:false, error:error.message}});
		}
	}

	complete(worker, data) {
		if (data.memory) worker.memory = data.memory;
		if (data.id == null || data.type) return;
		const started = worker.requests.get(data.id);
		worker.requests.delete(data.id);
		if (this.memory > this.budget) this.limit = Math.max(1, this.limit - 1);
		else if (started !== undefined && data.ok && performance.now() - started > 100 && this.memory + Math.max(128 << 20, worker.memory) < this.budget) this.limit = Math.min(this.capacity, this.limit + 1);
	}

	drain() {
		if (this.draining) return;
		this.draining = true;
		try {
			if (this.queue.length) for (const worker of [...this.workers].sort((a,b) => b.memory-a.memory)) {
				if (this.memory + (64 << 20) <= this.budget) break;
				if (!worker.requests.size) worker.reclaim?.();
			}
			this.queue.sort((a, b) => Number(a.worker.batch) - Number(b.worker.batch) + (a.queued - b.queued) / 2000);
			for (let i = 0; i < this.queue.length;) {
				const task = this.queue[i];
				if (task.worker.closed) { this.queue.splice(i, 1); continue; }
				const active = this.active();
				if (task.worker.requests.size || active >= this.limit || active && this.memory + (64 << 20) > this.budget) { i++; continue; }
				this.queue.splice(i, 1);
				this.send(task);
			}
		} finally { this.draining = false; }
	}

	cancel(worker, id) {
		const index = this.queue.findIndex(task => task.worker === worker && task.data.id === id);
		if (index < 0) return false;
		this.queue.splice(index, 1);
		worker.onmessage?.({data:{id, ok:false, canceled:true, error:"操作已取消"}});
		this.drain();
		return true;
	}

	remove(worker) {
		this.workers.delete(worker);
		this.queue = this.queue.filter(task => task.worker !== worker);
		worker.requests.clear();
		this.drain();
	}
}

class BrowserWorker {
	constructor(options) {
		engineScheduler ||= new BrowserScheduler();
		this.primary = !!options.primary;
		this.batch = !options.primary && !options.page;
		this.requests = new Map();
		this.memory = 64 << 20;
		this.reservation = 0;
		this.closed = false;
		this.worker = new Worker("./ofdgo_wasm.js");
		engineScheduler.workers.add(this);
		this.worker.onmessage = event => {
			if (this.closed || event.data.type === "module") return;
			engineScheduler.complete(this, event.data);
			if (event.data.type === "exit") this.terminate();
			try { this.onmessage?.(event); }
			finally { engineScheduler.drain(); }
		};
		this.worker.onerror = event => { this.terminate(); this.onerror?.(event); };
		this.worker.onmessageerror = event => { this.terminate(); this.onmessageerror?.(event); };
		this.worker.addEventListener("message", event => {
			if (event.data.type !== "module") return;
			event.stopImmediatePropagation();
			engineModule ||= WebAssembly.compileStreaming(fetch("./ofdgo.wasm")).catch(error => { engineModule = null; throw error; });
			engineModule.then(module => { if (!this.closed) this.worker.postMessage({ type:"module", module }); }, error => { if (!this.closed) this.worker.postMessage({ type:"module", error:error.message }); });
		});
	}

	postMessage(data, transfer) {
		if (this.closed) throw new DOMException("引擎已关闭", "AbortError");
		if (data.type) {
			if (data.type === "cancel" && engineScheduler.cancel(this, data.id)) return;
			this.worker.postMessage(data, transfer);
		} else engineScheduler.submit(this, data, transfer);
	}

	terminate() {
		if (this.closed) return;
		this.closed = true;
		this.worker.terminate();
		engineScheduler.remove(this);
	}
}

export function createEngineWorker(options = {}) {
	if (nativeHost) return nativeHost.worker();
	if (options.pages) return new BrowserEngine();
	return new BrowserWorker(options);
}

export async function documentBytes(file) {
	if (file?.nativeFile) return file;
	if (file instanceof Blob) return new Response(file.stream()).blob();
	return new Uint8Array(await file.arrayBuffer());
}

class BrowserEngine {
	constructor() {
		this.primary = createEngineWorker({primary:true});
		this.primary.onmessage = event => {
			const data = event.data;
			if (data.memory) this.memory = data.memory;
			if (this.source) this.parallelism = engineScheduler.limit;
			if (data.id != null && !data.type) this.requests.delete(data.id);
			this.onmessage?.(event);
		};
		this.primary.onerror = event => this.onerror?.(event);
		this.primary.onmessageerror = event => this.onmessageerror?.(event);
		this.requests = new Map();
		this.slots = new Set();
		this.sequence = 0;
		this.revision = 0;
		this.memory = 0;
		this.parallelism = 1;
		this.budget = engineScheduler.budget;
		this.closed = false;
		this.source = null;
	}

	configurePages(source) {
		this.invalidatePages();
		if (!source || this.closed) return;
		this.source = {...source, file: source.file instanceof Blob ? source.file : new Blob([source.file])};
		this.parallelism = engineScheduler.limit;
	}

	invalidatePages() {
		this.revision++;
		this.source = null;
		this.parallelism = 1;
		for (const slot of this.slots) this.closeSlot(slot);
	}

	closeSlot(slot, error = new DOMException("页面加载已取消", "AbortError")) {
		if (!this.slots.delete(slot)) return;
		slot.worker.terminate();
		slot.rejectReady(error);
		for (const pending of slot.pending.values()) pending.reject(error);
		slot.pending.clear();
	}

	createSlot(source) {
		const worker = createEngineWorker({page:true});
		const slot = {worker, pending: new Map(), busy: false, memory: Math.max(128 << 20, this.memory), pages:new Set(), lastPage:0};
		worker.reservation = slot.memory;
		worker.reclaim = () => { if (!slot.busy) this.closeSlot(slot); };
		const ready = new Promise((resolve, reject) => {
			slot.rejectReady = reject;
			worker.onmessage = ({data}) => {
				if (data.memory) slot.memory = data.memory;
				if (this.source) this.parallelism = engineScheduler.limit;
				if (data.type === "ready") { resolve(); return; }
				if (data.type === "exit") { this.closeSlot(slot, new Error("页面加载失败")); return; }
				if (data.type) return;
				const request = slot.pending.get(data.id);
				if (!request) return;
				slot.pending.delete(data.id);
				if (data.ok) request.resolve(data);
				else request.reject(Object.assign(new Error(data.error), {code: data.code}));
			};
			worker.onerror = worker.onmessageerror = () => this.closeSlot(slot, new Error("页面加载失败"));
		});
		this.slots.add(slot);
		slot.ready = ready.then(async () => {
			let opened = await this.callSlot(slot, "ofdgoOpen", [source.file, source.fonts, source.annotations]);
			if ((source.document || 0) !== (opened.data.documentIndex || 0)) opened = await this.callSlot(slot, "ofdgoSelectDocument", [source.document]);
			if ((source.version || "") !== (opened.data.documentVersion || "")) await this.callSlot(slot, "ofdgoSelectVersion", [source.version || ""]);
		});
		slot.ready.catch(() => {});
		return slot;
	}

	callSlot(slot, name, args) {
		return new Promise((resolve, reject) => {
			if (!this.slots.has(slot)) { reject(new DOMException("页面加载已取消", "AbortError")); return; }
			const id = --this.sequence;
			slot.pending.set(id, {resolve, reject});
			try { slot.worker.postMessage({id, name, args}); }
			catch (error) { slot.pending.delete(id); reject(error); }
		});
	}

	postMessage(data, transfer) {
		if (this.closed) return;
		if (data.type === "cancel") {
			const slot = this.requests.get(data.id);
			if (slot) this.closeSlot(slot);
			else this.primary.postMessage(data);
			return;
		}
		if (!data.type && data.name !== "ofdgoRenderPage" && ![
			"ofdgoDocumentInfo", "ofdgoDocumentVersions", "ofdgoDocumentNavigation", "ofdgoOpenLinks", "ofdgoOutlineLinks", "ofdgoRenderBackends", "ofdgoSVGFontData",
			"ofdgoSearchPage", "ofdgoPageTextString", "ofdgoResources", "ofdgoResourceReferences", "ofdgoResourceTargets",
			"ofdgoResourceLocations", "ofdgoFontPreview", "ofdgoFontFaces", "ofdgoFontFace", "ofdgoMatchFontFiles",
			"ofdgoImages", "ofdgoStructureTargets", "ofdgoStructureObjects", "ofdgoPackageDocuments", "ofdgoVerifySignatures",
			"ofdgoAnnotationInfos", "ofdgoInvoices", "ofdgoInvoice", "ofdgoExportInvoice", "ofdgoValidateInvoice",
			"ofdgoActionList", "ofdgoPageSettings", "ofdgoReadAnnotation", "ofdgoPreviewSignatureSeal",
			"ofdgoExportFormats", "ofdgoOutputFormats", "ofdgoParsePageRange", "ofdgoExportPage", "ofdgoExportDocument",
			"ofdgoExportAttachment", "ofdgoExportResource", "ofdgoExportMedia", "ofdgoExportVersion"
		].includes(data.name)) this.invalidatePages();
		const preferred = data.name === "ofdgoRenderPage" ? [...this.slots].find(slot => !slot.busy && slot.pages.has(data.args[0])) : null;
		if (data.name !== "ofdgoRenderPage" || !this.source || !preferred && ![...this.requests.values()].includes(null)) {
			if (data.name === "ofdgoRenderPage") this.requests.set(data.id, null);
			this.primary.postMessage(data, transfer);
			return;
		}
		let slot = preferred || [...this.slots].filter(slot => !slot.busy).sort((a,b) => Math.abs(a.lastPage-data.args[0])-Math.abs(b.lastPage-data.args[0]))[0];
		const memory = engineScheduler.memory;
		if (!slot && this.slots.size < this.parallelism - 1 && memory + Math.max(128 << 20, this.memory) <= this.budget) slot = this.createSlot(this.source);
		if (!slot) {
			this.requests.set(data.id, null);
			this.primary.postMessage(data, transfer);
			return;
		}
		slot.busy = true;
		this.requests.set(data.id, slot);
		const revision = this.revision;
		let fallback = false;
		slot.ready.then(() => this.callSlot(slot, data.name, data.args)).then(result => {
			if (revision !== this.revision) throw new DOMException("页面加载已取消", "AbortError");
			slot.lastPage = data.args[0];
			slot.pages.delete(slot.lastPage);
			slot.pages.add(slot.lastPage);
			if (slot.pages.size > 16) slot.pages.delete(slot.pages.values().next().value);
			if (!this.closed) this.onmessage?.({data: {...result, id: data.id}});
		}).catch(error => {
			if (!this.closed && revision === this.revision && error.name !== "AbortError") {
				this.closeSlot(slot, error);
				this.parallelism = Math.max(1, this.slots.size + 1);
				this.requests.set(data.id, null);
				fallback = true;
				this.primary.postMessage(data, transfer);
				return;
			}
			if (!this.closed) this.onmessage?.({data: {id: data.id, ok: false, error: error.message, canceled: error.name === "AbortError"}});
		}).finally(() => {
			if (!fallback) this.requests.delete(data.id);
			slot.busy = false;
			slot.worker.reservation = 0;
			const memory = engineScheduler.memory;
			if (memory > this.budget) { this.closeSlot(slot); this.parallelism = Math.max(1, this.slots.size + 1); }
			engineScheduler.drain();
		});
	}

	terminate() {
		if (this.closed) return;
		this.closed = true;
		this.invalidatePages();
		this.primary.terminate();
		this.requests.clear();
	}
}

export function saveFilePicker(options) {
	return nativeHost ? nativeHost.savePicker(options) : window.showSaveFilePicker ? window.showSaveFilePicker(options) : null;
}

export function directoryPicker() {
	return nativeHost ? nativeHost.directoryPicker() : window.showDirectoryPicker({mode:"readwrite"});
}

class NativeFile {
	constructor(value) {
		Object.assign(this, value);
		if (value.handle) this.handle = new NativeHandle(nativeHost, value.handle);
		this.byteLength = this.size;
		this.type ||= "application/octet-stream";
		if (value.temporary) nativeHost?.resources?.register(this, value.nativeFile);
	}

	async arrayBuffer(signal) {
		const response = await fetch(this.url, {signal});
		if (!response.ok) throw new Error("读取文件失败");
		return response.arrayBuffer();
	}

	toJSON() {
		return {nativeFile:this.nativeFile, size:this.size, name:this.name};
	}

	async text() {
		return new TextDecoder().decode(await this.arrayBuffer());
	}
}

class NativeHandle {
	constructor(host, value) {
		this.host = host;
		Object.assign(this, value);
	}

	toJSON() {
		return {nativeHandle:this.nativeHandle, name:this.name, kind:this.kind};
	}

	async getFile() {
		return new NativeFile(await this.host.call("file", {handle:this.nativeHandle}));
	}

	async getFileHandle(name, options = {}) {
		return new NativeHandle(this.host, await this.host.call("child", {handle:this.nativeHandle, name, create:!!options.create, directory:false}));
	}

	async getDirectoryHandle(name, options = {}) {
		return new NativeHandle(this.host, await this.host.call("child", {handle:this.nativeHandle, name, create:!!options.create, directory:true}));
	}

	removeEntry(name, options = {}) {
		return this.host.call("remove", {handle:this.nativeHandle, name, recursive:!!options.recursive});
	}

	async createWritable() {
		const {writer} = await this.host.call("write.begin", {handle:this.nativeHandle});
		return {
			write: data => this.host.call("write.data", {writer, data}),
			close: () => this.host.call("write.commit", {writer}),
			abort: () => this.host.call("write.abort", {writer}),
		};
	}
}

class NativeHost {
	constructor(bridge) {
		this.bridge = bridge;
		this.parallelism = 1;
		this.cacheBytes = 32 * 1024 * 1024;
		this.reading = 0;
		this.readQueue = [];
		this.resources = typeof FinalizationRegistry === "function" ? new FinalizationRegistry(file => {
			this.call("release", {file}).catch(() => {});
		}) : null;
	}

	call(name, value, signal) {
		return this.bridge.call(`ofdgo.host.${name}`, value, {signal});
	}

	async readResource(file, signal) {
		if (this.reading >= this.parallelism) await new Promise(resolve => this.readQueue.push(resolve));
		else this.reading++;
		try {
			signal?.throwIfAborted();
			return await file.arrayBuffer(signal);
		} finally {
			try { await this.call("release", {file:file.nativeFile}); }
			finally {
				const next = this.readQueue.shift();
				if (next) next();
				else this.reading--;
			}
		}
	}

	async decode(value, name = "", key = "", signal) {
		const reads = [];
		const visit = (item, field, replace) => {
			if (!item || typeof item !== "object" || item instanceof Blob || item instanceof ArrayBuffer || ArrayBuffer.isView(item)) return;
			if (item.nativeFile) {
				const file = new NativeFile(item);
				if (field === "file" || name === "ofdgoConvertPDF" && field === "bytes") replace(file);
				else reads.push(this.readResource(file, signal).then(bytes => replace(field === "blob" ? new Blob([bytes], {type:item.type}) : new Uint8Array(bytes))));
				return;
			}
			for (const [child, content] of Object.entries(item)) {
				if (content && typeof content === "object") visit(content, Array.isArray(item) ? "" : child, result => item[child] = result);
			}
		};
		visit(value, key, result => value = result);
		const results = await Promise.allSettled(reads);
		const failed = results.find(result => result.status === "rejected");
		if (failed) throw failed.reason;
		return value;
	}

	async release(value) {
		if (value instanceof Blob || value instanceof ArrayBuffer || ArrayBuffer.isView(value)) return;
		if (value?.nativeFile && value.temporary) {
			await this.call("release", {file:value.nativeFile}).catch(() => {});
			return;
		}
		if (value && typeof value === "object") {
			for (const item of Object.values(value)) await this.release(item);
		}
	}

	worker() {
		const host = this;
		let closed = false;
		let unsubscribe;
		let pending = Promise.resolve();
		const readers = new Set();
		const requests = new Map();
		const completions = new Map();
		const emit = data => {
			if (data.type === "result") { completions.get(data.id)?.(data.result); return; }
			if (!closed) worker.onmessage?.({data});
		};
		const ready = host.call("engine.open", {}).then(({engine, parallelism, cacheBytes}) => {
			if (closed) return engine;
			host.parallelism = Math.max(1, parallelism || 1);
			host.cacheBytes = cacheBytes || host.cacheBytes;
			unsubscribe = host.bridge.on(`ofdgo.engine.${engine}`, emit);
			emit({type:"ready"});
			return engine;
		}).catch(error => { if (!closed) worker.onerror?.({message:error.message}); throw error; });
		const worker = {
			onmessage:null,
			onerror:null,
			postMessage(data) {
				if (closed) return;
				if (data.type === "cancel") {
					requests.get(data.id)?.abort();
					return;
				}
				if (data.type === "font") return;
				const controller = new AbortController();
				requests.set(data.id, controller);
				const parallel = data.name === "ofdgoRenderPage";
				const barrier = parallel ? pending : Promise.all([pending, ...readers]);
				const operation = barrier.then(async () => {
					const engine = await ready;
					if (closed) return;
					controller.signal.throwIfAborted();
					const args = {engine, id:data.id, name:data.name, args:data.args};
					let result;
					if (data.name === "ofdgoConvertFile" || data.name === "ofdgoPackFiles") {
						const completed = new Promise(resolve => completions.set(data.id, resolve));
						const cancel = () => host.call("engine.cancel", {engine, id:data.id}).catch(() => {});
						controller.signal.addEventListener("abort", cancel, {once:true});
						try {
							await host.call("engine.start", args);
							if (controller.signal.aborted) cancel();
							result = await completed;
							await host.call("engine.finish", {engine, id:data.id});
						} finally {
							controller.signal.removeEventListener("abort", cancel);
							completions.delete(data.id);
						}
					} else result = await host.call("engine.call", args, controller.signal);
					if (closed) { await host.release(result); return; }
					let decoded;
					try {
						controller.signal.throwIfAborted();
						decoded = await host.decode(result, data.name, "", controller.signal);
						controller.signal.throwIfAborted();
					}
					catch (error) { await host.release(result); throw error; }
					if (closed) { await host.release(result); return; }
					emit({id:data.id, ...decoded});
				}).catch(error => emit({id:data.id, ok:false, error:error.message, code:error.code, canceled:error.name === "AbortError"})).finally(() => requests.delete(data.id));
				if (parallel) {
					readers.add(operation);
					operation.finally(() => readers.delete(operation));
				} else if (data.name !== "ofdgoSaveSnapshot") pending = operation;
			},
			terminate() {
				if (closed) return;
				closed = true;
				for (const controller of requests.values()) controller.abort();
				ready.then(engine => host.call("engine.close", {engine})).catch(() => {}).finally(() => {
					unsubscribe?.();
					for (const resolve of completions.values()) resolve({ok:false, canceled:true, error:"会话已关闭"});
					completions.clear();
				});
			},
		};
		ready.catch(() => {});
		return worker;
	}

	async savePicker(options) {
		return new NativeHandle(this, await this.call("save", options));
	}

	async directoryPicker() {
		return new NativeHandle(this, await this.call("directory", {}));
	}

	async fonts() {
		return (await this.call("fonts", {})).map(({file, ...font}) => ({...font, nativeFile:new NativeFile(file)}));
	}

	async start(onFiles, onError) {
		this.bridge.on("ofdgo.files", values => onFiles(values.map(value => new NativeFile(value))));
		this.bridge.on("ofdgo.error", message => onError(new Error(message)));
		const title = document.querySelector("title");
		if (title) new MutationObserver(() => this.call("title", {title:document.title}).catch(onError)).observe(title, {childList:true});
		await this.call("ready", {});
	}

	installFileInputs(onError) {
		document.addEventListener("click", async event => {
			const input = event.target;
			if (!(input instanceof HTMLInputElement) || input.type !== "file" || input.disabled) return;
			event.preventDefault();
			try {
				const files = (await this.call("open", {multiple:input.multiple, directory:input.webkitdirectory, accept:input.accept})).map(value => new NativeFile(value));
				if (!files.length) return;
				const transfer = new DataTransfer();
				for (const file of files) transfer.items.add(new File([], file.name, {type:file.type}));
				delete input.files;
				input.files = transfer.files;
				Object.defineProperty(input, "files", {configurable:true, get:() => input.value ? files : []});
				input.dispatchEvent(new Event("input", {bubbles:true}));
				input.dispatchEvent(new Event("change", {bubbles:true}));
			} catch (error) { if (error.name !== "AbortError") onError(error); }
		}, true);
	}

	async download(data, mime, name) {
		const handle = await this.savePicker({suggestedName:name});
		const writer = await handle.createWritable();
		try { await writer.write(data); await writer.close(); }
		catch (error) { await writer.abort().catch(() => {}); throw error; }
	}
}

export const nativeHost = globalThis.guigo ? new NativeHost(globalThis.guigo) : null;
