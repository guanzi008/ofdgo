import "core-js/actual/array/at";
import "core-js/actual/array/find-last";
import "core-js/actual/string/at";
import "core-js/actual/string/replace-all";
import "core-js/actual/typed-array/at";
import "core-js/actual/object/from-entries";
import "core-js/actual/promise/all-settled";
import "unicode-segmenter/intl-polyfill";
import "abortcontroller-polyfill/dist/polyfill-patch-fetch";
import "wicg-inert";
import structuredClonePolyfill from "@ungap/structured-clone";
import ResizeObserverPolyfill from "resize-observer-polyfill";
import dialogPolyfill from "dialog-polyfill";
import { EventTarget as EventTargetPolyfill } from "event-target-shim";

for (const Input of [HTMLInputElement, HTMLTextAreaElement]) {
    for (const name of ["selectionStart", "selectionEnd", "selectionDirection"]) {
        const descriptor = Object.getOwnPropertyDescriptor(Input.prototype, name);
        if (!descriptor?.get || !descriptor.configurable) continue;
        Object.defineProperty(Input.prototype, name, {
            ...descriptor,
            get() {
                try { return descriptor.get.call(this); }
                catch (error) { if (this instanceof Input) return null; throw error; }
            }
        });
    }
}
try { new EventTarget(); } catch { globalThis.EventTarget = EventTargetPolyfill; }
if (!crypto.randomUUID) crypto.randomUUID = function () {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = bytes[6] & 15 | 64;
    bytes[8] = bytes[8] & 63 | 128;
    const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
};

if (!globalThis.structuredClone) globalThis.structuredClone = structuredClonePolyfill;
if (!globalThis.ResizeObserver) globalThis.ResizeObserver = ResizeObserverPolyfill;
if (!("inset" in document.documentElement.style)) Object.defineProperty(CSSStyleDeclaration.prototype, "inset", {
    configurable: true,
    get() { return `${this.top} ${this.right} ${this.bottom} ${this.left}`; },
    set(value) {
        const parts = String(value).trim().split(/\s+/);
        this.top = parts[0];
        this.right = parts[1] || parts[0];
        this.bottom = parts[2] || parts[0];
        this.left = parts[3] || parts[1] || parts[0];
    }
});
if (!Blob.prototype.arrayBuffer) Blob.prototype.arrayBuffer = function () {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result);
        reader.onerror = () => reject(reader.error);
        reader.readAsArrayBuffer(this);
    });
};
if (!Blob.prototype.text) Blob.prototype.text = function () {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result);
        reader.onerror = () => reject(reader.error);
        reader.readAsText(this);
    });
};
if (!AbortSignal.prototype.throwIfAborted) AbortSignal.prototype.throwIfAborted = function () {
    if (this.aborted) throw this.reason || new DOMException("Aborted", "AbortError");
};
if (!AbortSignal.timeout) AbortSignal.timeout = function (delay) {
    const controller = new AbortController();
    setTimeout(() => controller.abort(new DOMException("Timed out", "TimeoutError")), delay);
    return controller.signal;
};
for (const prototype of [Element.prototype, Document.prototype, DocumentFragment.prototype]) {
    if (!prototype.replaceChildren) prototype.replaceChildren = function (...children) {
        while (this.firstChild) this.removeChild(this.firstChild);
        this.append(...children);
    };
}
const mediaPrototype = Object.getPrototypeOf(matchMedia("all"));
if (!mediaPrototype.addEventListener) {
    mediaPrototype.addEventListener = function (type, listener) {
        if (type === "change") this.addListener(listener);
    };
    mediaPrototype.removeEventListener = function (type, listener) {
        if (type === "change") this.removeListener(listener);
    };
}
export function registerDialog(dialog) {
    if (typeof dialog.show !== "function") dialogPolyfill.registerDialog(dialog);
}
const registerDialogs = () => document.querySelectorAll("dialog").forEach(registerDialog);
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", registerDialogs);
else registerDialogs();
