const assert = require("node:assert/strict");
const { Segmenter } = require("unicode-segmenter/intl-adapter");

const samples = [
    ["abc", ["a", "b", "c"]],
    ["中文排版", ["中", "文", "排", "版"]],
    ["e\u0301", ["e\u0301"]],
    ["a\u0308\u0301", ["a\u0308\u0301"]],
    ["👨‍👩‍👧‍👦", ["👨‍👩‍👧‍👦"]],
    ["🇨🇳🇺🇸", ["🇨🇳", "🇺🇸"]],
    ["👍🏽", ["👍🏽"]],
    ["क्‍ष", ["क्‍ष"]],
    ["\r\n", ["\r\n"]],
    ["a👩🏽‍💻中e\u0301", ["a", "👩🏽‍💻", "中", "e\u0301"]]
];

function check(Constructor) {
    const segmenter = new Constructor(undefined, { granularity: "grapheme" });
    for (const [text, expected] of samples) {
        const segments = Array.from(segmenter.segment(text));
        assert.deepEqual(segments.map(part => part.segment), expected, text);
        let offset = 0;
        for (const part of segments) {
            assert.equal(part.index, offset, text);
            offset += part.segment.length;
        }
        assert.equal(offset, text.length);
    }
}

check(Segmenter);
const original = Intl.Segmenter;
require("unicode-segmenter/intl-polyfill");
assert.equal(Intl.Segmenter, original);
delete Intl.Segmenter;
delete require.cache[require.resolve("unicode-segmenter/intl-polyfill")];
require("unicode-segmenter/intl-polyfill");
assert.equal(typeof Intl.Segmenter, "function");
check(Intl.Segmenter);
console.log("字符分段、UTF-16 位置和原生实现保留检查通过。");
