import assert from "node:assert/strict";
import { test } from "node:test";
import { observePopupSize } from "../src/lib/popup-size.ts";

test("popup height updates while animation frames are paused, rounds up, and skips duplicate sizes", (t) => {
    let changed, observed, disconnected = false;
    const frame = globalThis.requestAnimationFrame, observer = globalThis.ResizeObserver, mutation = globalThis.MutationObserver;
    t.after(() => {
        if (frame === undefined) delete globalThis.requestAnimationFrame; else globalThis.requestAnimationFrame = frame;
        if (observer === undefined) delete globalThis.ResizeObserver; else globalThis.ResizeObserver = observer;
        if (mutation === undefined) delete globalThis.MutationObserver; else globalThis.MutationObserver = mutation;
    });
    globalThis.requestAnimationFrame = () => { throw new Error("Animation frames are paused"); };
    globalThis.ResizeObserver = class {
        constructor(fn) { changed = fn; }
        observe(card) { observed = card; }
        disconnect() { disconnected = true; }
    };
    globalThis.MutationObserver = class { observe() {} disconnect() {} };
    let height = 520.2;
    const card = { getBoundingClientRect: () => ({ height }) };
    const sizes = [];
    const stop = observePopupSize(card, (value) => sizes.push(value));
    assert.equal(observed, card);
    changed(); changed();
    height = 0; changed();
    height = 300; changed();
    assert.deepEqual(sizes, [521, 300]);
    stop(); assert.equal(disconnected, true);
});

test("DOM changes resize a hidden popup even when render-based observers never fire", (t) => {
    const originalResize = globalThis.ResizeObserver, originalMutation = globalThis.MutationObserver;
    t.after(() => {
        if (originalResize === undefined) delete globalThis.ResizeObserver; else globalThis.ResizeObserver = originalResize;
        if (originalMutation === undefined) delete globalThis.MutationObserver; else globalThis.MutationObserver = originalMutation;
    });
    let mutated;
    globalThis.ResizeObserver = class { observe() {} disconnect() {} };
    globalThis.MutationObserver = class { constructor(fn) { mutated = fn; } observe() {} disconnect() {} };
    let height = 240;
    const sizes = [];
    const stop = observePopupSize({ getBoundingClientRect: () => ({ height }) }, (value) => sizes.push(value));
    assert.equal(typeof mutated, "function", "A hidden popup must observe its DOM without waiting for a rendering frame");
    height = 520; mutated();
    assert.equal(sizes.at(-1), 520);
    stop();
});
