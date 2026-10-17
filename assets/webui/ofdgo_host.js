let engineModule = null;

export function createEngineWorker() {
	if (nativeHost) return nativeHost.worker();
	const worker = new Worker("./ofdgo_wasm.js");
	worker.addEventListener("message", event => {
		if (event.data.type !== "module") return;
		event.stopImmediatePropagation();
		engineModule ||= WebAssembly.compileStreaming(fetch("./ofdgo.wasm")).catch(error => { engineModule = null; throw error; });
		engineModule.then(module => worker.postMessage({ type: "module", module }), error => worker.postMessage({ type: "module", error: error.message }));
	});
	return worker;
}

export async function documentBytes(file) {
	return file?.nativeFile ? file : new Uint8Array(await file.arrayBuffer());
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
		this.resources = typeof FinalizationRegistry === "function" ? new FinalizationRegistry(file => {
			this.call("release", {file}).catch(() => {});
		}) : null;
	}

	call(name, value, signal) {
		return this.bridge.call(`ofdgo.host.${name}`, value, {signal});
	}

	async decode(value, name = "", key = "", signal) {
		if (value?.nativeFile) {
			const file = new NativeFile(value);
			if (key === "file" || name === "ofdgoConvertPDF" && key === "bytes") return file;
			try {
				const bytes = await file.arrayBuffer(signal);
				return key === "blob" ? new Blob([bytes], {type:value.type}) : new Uint8Array(bytes);
			} finally {
				await this.call("release", {file:value.nativeFile});
			}
		}
		if (Array.isArray(value)) {
			for (let i = 0; i < value.length; i++) value[i] = await this.decode(value[i], name, "", signal);
			return value;
		}
		if (value && typeof value === "object") {
			for (const [field, item] of Object.entries(value)) value[field] = await this.decode(item, name, field, signal);
			return value;
		}
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
		const requests = new Map();
		const completions = new Map();
		const emit = data => {
			if (data.type === "result") { completions.get(data.id)?.(data.result); return; }
			if (!closed) worker.onmessage?.({data});
		};
		const ready = host.call("engine.open", {}).then(({engine}) => {
			if (closed) return engine;
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
				pending = pending.then(async () => {
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
		return (await this.call("fonts", {})).map(font => ({...font, nativeFile:new NativeFile(font.file)}));
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
