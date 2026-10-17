export function createEngineWorker() {
	return nativeHost ? nativeHost.worker() : new Worker("./ofdgo_wasm.js");
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

	async arrayBuffer() {
		const response = await fetch(this.url);
		if (!response.ok) throw new Error("读取文件失败");
		return response.arrayBuffer();
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
			write: async data => this.host.call("write.data", {writer, data:await this.host.encode(data)}),
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

	call(name, value) {
		return this.bridge.call(`ofdgo.host.${name}`, value);
	}

	async encode(value, key = "") {
		if (value?.nativeFile) return {nativeFile:value.nativeFile, size:value.size, name:value.name};
		if (value?.nativeHandle) return {nativeHandle:value.nativeHandle, name:value.name, kind:value.kind};
		if (value instanceof Blob || value instanceof ArrayBuffer || ArrayBuffer.isView(value)) {
			if (!(value instanceof Blob)) {
				const bytes = ArrayBuffer.isView(value) ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength) : new Uint8Array(value);
				if (bytes.byteLength <= 65536 || ["password", "key", "keyPassword", "certificate"].includes(key)) return {nativeBytes:Array.from(bytes)};
			}
			const response = await fetch("/__native/input", {method:"POST", body:value});
			if (!response.ok) throw new Error("传递数据失败");
			return response.json();
		}
		if (Array.isArray(value)) return Promise.all(value.map(item => this.encode(item)));
		if (value && typeof value === "object") {
			const result = {};
			for (const [key, item] of Object.entries(value)) result[key] = await this.encode(item, key);
			return result;
		}
		return value;
	}

	async decode(value, name = "", key = "") {
		if (value?.nativeFile) {
			const file = new NativeFile(value);
			if (key === "file" || name === "ofdgoConvertPDF" && key === "bytes") return file;
			try {
				const bytes = await file.arrayBuffer();
				return key === "blob" ? new Blob([bytes], {type:value.type}) : new Uint8Array(bytes);
			} finally {
				await this.call("release", {file:value.nativeFile});
			}
		}
		if (Array.isArray(value)) return Promise.all(value.map(item => this.decode(item, name)));
		if (value && typeof value === "object") {
			const result = {};
			for (const [field, item] of Object.entries(value)) result[field] = await this.decode(item, name, field);
			return result;
		}
		return value;
	}

	worker() {
		const host = this;
		let closed = false;
		let unsubscribe;
		let pending = Promise.resolve();
		const canceled = new Set();
		const emit = data => { if (!closed) worker.onmessage?.({data}); };
		const ready = host.call("engine.open", {}).then(({engine}) => {
			if (closed) { host.call("engine.close", {engine}).catch(() => {}); return engine; }
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
					canceled.add(data.id);
					ready.then(engine => host.call("engine.cancel", {engine, id:data.id})).catch(() => {});
					return;
				}
				if (data.type === "font") return;
				pending = pending.then(async () => {
					const engine = await ready;
					if (closed) return;
					if (canceled.has(data.id)) throw Object.assign(new Error("操作已取消"), {name:"AbortError"});
					const args = await host.encode(data.args);
					if (closed) return;
					if (canceled.has(data.id)) throw Object.assign(new Error("操作已取消"), {name:"AbortError"});
					const result = await host.call("engine.call", {engine, id:data.id, name:data.name, args});
					emit({id:data.id, ...await host.decode(result, data.name)});
				}).catch(error => emit({id:data.id, ok:false, error:error.message, code:error.code, canceled:error.name === "AbortError"})).finally(() => canceled.delete(data.id));
			},
			terminate() {
				if (closed) return;
				closed = true;
				unsubscribe?.();
				ready.then(engine => host.call("engine.close", {engine})).catch(() => {});
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
