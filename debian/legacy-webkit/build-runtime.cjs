const esbuild = require("esbuild");
const fs = require("node:fs");
const path = require("node:path");
esbuild.buildSync({
    entryPoints: [path.join(__dirname, "runtime-entry.js")],
    outfile: path.join(__dirname, "runtime.js"),
    bundle: true,
    format: "iife",
    globalName: "ofdgoLegacy",
    target: "safari13.1",
    supported: { "template-literal": false },
    minify: true,
    legalComments: "inline"
});
fs.copyFileSync(require.resolve("dialog-polyfill/dist/dialog-polyfill.css"), path.join(__dirname, "dialog.css"));
const packages = ["core-js", "abortcontroller-polyfill", "@ungap/structured-clone", "resize-observer-polyfill", "dialog-polyfill", "wicg-inert", "event-target-shim", "unicode-segmenter"];
let notices = "OFDGo legacy WebKit compatibility libraries\n";
for (const name of packages) {
    let root = path.dirname(require.resolve(name));
    while (!fs.existsSync(path.join(root, "package.json")) || JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8")).name !== name) {
        const parent = path.dirname(root);
        if (parent === root) throw new Error("Missing package root: " + name);
        root = parent;
    }
    const license = ["LICENSE", "LICENSE.md", "LICENSE.txt"].map(file => path.join(root, file)).find(file => fs.existsSync(file));
    if (!license) throw new Error("Missing license: " + name);
    notices += "\n" + name + "\n" + fs.readFileSync(license, "utf8") + "\n";
}
fs.writeFileSync(path.join(__dirname, "NOTICE"), notices);
