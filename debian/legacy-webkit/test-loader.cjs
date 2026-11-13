const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const loader = fs.readFileSync(path.join(__dirname, "loader.js"), "utf8");
const setup = `
    window = this;
    var writes = [];
    function HTMLElement() {}
    HTMLElement.prototype.inert = false;
    function Element() {}
    function Document() {}
    function DocumentFragment() {}
    for (var C of [Element, Document, DocumentFragment]) C.prototype.replaceChildren = function () {};
    function AbortSignal() {}
    AbortSignal.prototype.throwIfAborted = function () {};
    AbortSignal.timeout = function () {};
    function EventTarget() {}
    function ResizeObserver() {}
    function structuredClone() {}
    function Blob() {}
    Blob.prototype.arrayBuffer = Blob.prototype.text = function () {};
    var crypto = { randomUUID: function () {} };
    var media = { addEventListener: function () {} };
    function matchMedia() { return media; }
    var document = {
        documentElement: { style: { inset: '' } },
        createElement: function (tag) { return tag === 'dialog' ? { show: function () {} } : {}; },
        write: function (html) { writes.push(html); }
    };
`;

function run(change = "") {
    const context = vm.createContext();
    vm.runInContext(setup + change, context);
    vm.runInContext(loader, context);
    return Array.from(context.writes);
}

assert.deepEqual(run(), []);
for (const missing of [
    "Array.prototype.at", "Array.prototype.findLast", "String.prototype.at", "String.prototype.replaceAll",
    "Uint8Array.prototype.at", "Object.fromEntries", "Promise.allSettled", "Intl.Segmenter",
    "AbortSignal.prototype.throwIfAborted", "AbortSignal.timeout", "HTMLElement.prototype.inert",
    "window.structuredClone", "window.ResizeObserver", "crypto.randomUUID", "Blob.prototype.arrayBuffer",
    "Blob.prototype.text", "Element.prototype.replaceChildren", "Document.prototype.replaceChildren",
    "DocumentFragment.prototype.replaceChildren", "media.addEventListener", "document.documentElement.style.inset"
]) {
    const change = missing.endsWith(".inert") || missing.endsWith(".inset") ? `delete ${missing};` : `${missing} = undefined;`;
    assert.deepEqual(run(change), ['<script src="./ofdgo_legacy.js"></script>'], missing);
}
assert.equal(run("document.createElement = function () { return {}; };").length, 1);
assert.equal(run("EventTarget = function () { throw new TypeError(); };").length, 1);
assert.equal(run("document.createElement = function (tag) { return tag === 'dialog' ? { show: function () {} } : Object.defineProperty({}, 'selectionStart', {get: function () { throw new TypeError(); }}); };").length, 1);
assert.equal(run("window.AbortSignal = undefined;").length, 1);
console.log("原生 API 完整时跳过兼容脚本；缺失 API 和旧版输入框 getter 均回退。");
