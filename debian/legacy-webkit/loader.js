(function () {
    var ready = false;
    try {
        ready = Array.prototype.at && Array.prototype.findLast && String.prototype.at &&
            String.prototype.replaceAll && Uint8Array.prototype.at && Object.fromEntries &&
            Promise.allSettled && Intl.Segmenter && AbortSignal.prototype.throwIfAborted &&
            AbortSignal.timeout && "inert" in HTMLElement.prototype && window.structuredClone &&
            window.ResizeObserver && typeof document.createElement("dialog").show === "function" &&
            crypto.randomUUID && Blob.prototype.arrayBuffer && Blob.prototype.text &&
            Element.prototype.replaceChildren && Document.prototype.replaceChildren &&
            DocumentFragment.prototype.replaceChildren && matchMedia("all").addEventListener &&
            "inset" in document.documentElement.style;
        if (ready) {
            new EventTarget();
            var input = document.createElement("input");
            input.type = "file";
            input.selectionStart;
        }
    } catch (_) {
        ready = false;
    }
    if (!ready) document.write('<script src="./ofdgo_legacy.js"><\/script>');
})();
