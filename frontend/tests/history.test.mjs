import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import ts from "typescript";
import { compileModule } from "svelte/compiler";

registerHooks({
    resolve(specifier, context, next) {
        if (specifier === "@wailsio/runtime" && context.parentURL?.endsWith("/format.ts")) {
            return { url: 'data:text/javascript,export const Browser = { OpenURL: (url) => globalThis.historyOpenedUrls.push(url) }; export const Clipboard = {};', shortCircuit: true };
        }
        return next(specifier, context);
    },
    load(url, context, next) {
        if (new URL(url).pathname.endsWith("/format.ts")) {
            return { format: "module", shortCircuit: true, source: ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText };
        }
        if (new URL(url).pathname.endsWith("history.svelte.ts")) {
            const source = ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText;
            return { format: "module", shortCircuit: true, source: compileModule(source, { filename: new URL(url).pathname }).js.code };
        }
        return next(url, context);
    },
});
const { HistoryBrowser } = await import("../src/lib/history.svelte.ts");
const { highlight, snippetParts, dayLabel, externalLinks } = await import("../src/lib/format.ts");
const chat = (id) => ({ id, title: `Chat ${id}`, updatedAt: "2026-10-24T10:00:00Z" });
const hit = (id) => ({ chat: chat(id), snippet: `A \x02match\x03 in ${id}` });
const detail = (id) => ({ chat: chat(id), messages: [{ id: `message-${id}`, role: "assistant", content: "Saved answer" }] });
const settle = () => new Promise((resolve) => setImmediate(resolve));
async function load(browser, query = "") {
    browser.search(query, 0);
    await new Promise((resolve) => setTimeout(resolve, 0));
    await settle();
}
function deferred() {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}
function setup(t, overrides = {}) {
    const calls = { opened: [], removed: [], deleted: [] };
    const browser = new HistoryBrowser({
        recent: async () => [chat("a"), chat("b")],
        search: async () => [hit("a"), hit("b")],
        get: async (id) => detail(id),
        open: async (id) => { calls.opened.push(id); },
        remove: async (id) => { calls.removed.push(id); },
        deleted: async (id) => { calls.deleted.push(id); },
        ...overrides,
    });
    t.after(() => browser.dispose());
    return { browser, calls };
}

test("history loads recent chats and previews the first result", async (t) => {
    const { browser } = setup(t);
    await load(browser);
    assert.equal(browser.loading, false);
    assert.equal(browser.selected, 0);
    assert.equal(browser.detail.chat.id, "a");
});

test("search debounces superseded input and retains snippets", async (t) => {
    const queries = [];
    const { browser } = setup(t, { search: async (q) => { queries.push(q); return [hit(q)]; } });
    browser.search("old", 150);
    await load(browser, "latest");
    assert.deepEqual(queries, ["latest"]);
    assert.equal(browser.hits[0].snippet, hit("latest").snippet);
});

test("a superseded search and its rejection cannot overwrite newer results", async (t) => {
    const old = deferred();
    const { browser } = setup(t, { search: (q) => q === "old" ? old.promise : Promise.resolve([hit("new")]) });
    await load(browser, "old");
    await load(browser, "new");
    old.reject(new Error("Old search failed"));
    await settle();
    assert.equal(browser.hits[0].chat.id, "new");
    assert.equal(browser.detail.chat.id, "new");
    assert.equal(browser.error, "");
});

test("preview clears on selection and ignores a late response", async (t) => {
    const old = deferred();
    const { browser } = setup(t, { get: (id) => id === "a" ? old.promise : Promise.resolve(detail(id)) });
    await load(browser);
    assert.equal(browser.previewLoading, true);
    await browser.select(1);
    old.resolve(detail("a"));
    await settle();
    assert.equal(browser.selected, 1);
    assert.equal(browser.detail.chat.id, "b");
});

test("keyboard navigation stays in bounds and Enter continues the selected chat", async (t) => {
    const { browser, calls } = setup(t);
    assert.equal(browser.keydown("Enter"), false);
    await load(browser);
    browser.keydown("ArrowUp");
    assert.equal(browser.selected, 0);
    browser.keydown("ArrowDown");
    browser.keydown("ArrowDown");
    assert.equal(browser.selected, 1);
    browser.keydown("Enter");
    await settle();
    assert.deepEqual(calls.opened, ["b"]);
});

test("deletion refreshes recent chats, selects the next result, and clears the last preview", async (t) => {
    const { browser, calls } = setup(t);
    await load(browser);
    await browser.removeSelected();
    await settle();
    assert.deepEqual(calls.deleted, ["a"]);
    assert.equal(browser.detail.chat.id, "b");
    assert.equal(browser.selected, 0);
    browser.keydown("Delete");
    await settle();
    assert.equal(browser.hits.length, 0);
    assert.equal(browser.detail, null);
    assert.equal(browser.selected, -1);
    assert.equal(browser.keydown("ArrowDown"), false);
});

test("Delete edits a nonempty search instead of deleting a chat", async (t) => {
    const { browser, calls } = setup(t);
    await load(browser, "query");
    assert.equal(browser.keydown("Delete"), false);
    assert.deepEqual(calls.removed, []);
});

test("failed search, preview, and deletion expose recoverable errors", async (t) => {
    const { browser } = setup(t, {
        search: async () => { throw new Error("Search failed"); },
        get: async () => { throw new Error("Preview failed"); },
        remove: async () => { throw new Error("Delete failed"); },
    });
    await load(browser, "query");
    assert.equal(browser.error, "Search failed");
    assert.equal(browser.loading, false);
    await load(browser);
    assert.equal(browser.previewError, "Preview failed");
    await browser.removeSelected();
    assert.equal(browser.error, "Delete failed");
    assert.equal(browser.hits.length, 2);
    assert.equal(browser.deleting, "");
});

test("deleting during a pending preview cannot restore the deleted chat", async (t) => {
    const preview = deferred();
    const { browser } = setup(t, { get: (id) => id === "a" ? preview.promise : Promise.resolve(detail(id)) });
    await load(browser);
    await browser.removeSelected();
    await settle();
    preview.resolve(detail("a"));
    await settle();
    assert.equal(browser.detail.chat.id, "b");
});

test("unmount cancels the debounce and ignores pending previews", async (t) => {
    const preview = deferred();
    const { browser } = setup(t, { get: () => preview.promise });
    await load(browser);
    browser.dispose();
    preview.resolve(detail("a"));
    await settle();
    assert.equal(browser.detail, null);
});

test("unmount cancels a search before it starts", async (t) => {
    let requested = false;
    const { browser } = setup(t, { search: async () => { requested = true; return []; } });
    browser.search("query", 0);
    browser.dispose();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(requested, false);
});

test("single-result navigation stays selected and empty results cannot open or delete", async (t) => {
    const { browser, calls } = setup(t, { recent: async () => [chat("only")], search: async () => [] });
    await load(browser);
    browser.keydown("ArrowDown");
    browser.keydown("ArrowUp");
    assert.equal(browser.selected, 0);
    await load(browser, "missing");
    assert.equal(browser.selected, -1);
    assert.equal(browser.detail, null);
    assert.equal(browser.keydown("Enter"), false);
    assert.equal(browser.keydown("Delete"), false);
    assert.deepEqual(calls.opened, []);
    assert.deepEqual(calls.removed, []);
});

test("title highlighting treats punctuation as literal text", () => {
    for (const query of ["[Go]", "a+b", "a.b", "(test)", "path\\file"]) {
        const title = `Before ${query} after`;
        const parts = highlight(title, query);
        assert.equal(parts.map((part) => part.text).join(""), title);
        assert.deepEqual(parts.filter((part) => part.hit).map((part) => part.text), [query]);
    }
});

test("snippet markers and date groups produce readable result labels", () => {
    assert.deepEqual(snippetParts("A \x02match\x03 here"), [
        { text: "A ", hit: false }, { text: "match", hit: true }, { text: " here", hit: false },
    ]);
    const now = new Date(2026, 9, 24, 12);
    assert.equal(dayLabel(new Date(2026, 9, 24).toISOString(), now), "Today");
    assert.equal(dayLabel(new Date(2026, 9, 23).toISOString(), now), "Yesterday");
    assert.equal(dayLabel(new Date(2026, 9, 1).toISOString(), now), "Earlier");
});

test("preview links prevent popup navigation and request the external browser", () => {
    globalThis.historyOpenedUrls = [];
    let listener;
    let prevented = false;
    const anchor = { getAttribute: () => "https://example.com/answer" };
    const node = {
        contains: (target) => target === anchor,
        addEventListener: (_, fn) => { listener = fn; },
        removeEventListener: (_, fn) => { assert.equal(fn, listener); listener = null; },
    };
    const action = externalLinks(node);
    listener({ target: { closest: () => anchor }, preventDefault: () => { prevented = true; } });
    assert.equal(prevented, true);
    assert.deepEqual(globalThis.historyOpenedUrls, ["https://example.com/answer"]);
    action.destroy();
    assert.equal(listener, null);
});
