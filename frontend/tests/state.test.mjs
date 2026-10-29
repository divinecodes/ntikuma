import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import ts from "typescript";
import { compileModule } from "svelte/compiler";

const api = new URL("../src/lib/api.ts", import.meta.url).href;
registerHooks({
    resolve(specifier, context, next) {
        if (specifier === "./api" && context.parentURL && new URL(context.parentURL).pathname.endsWith("state.svelte.ts")) {
            return { url: api, shortCircuit: true };
        }
        return next(specifier, context);
    },
    load(url, context, next) {
        if (url === api) {
            return { format: "module", shortCircuit: true, source: `
                const h = globalThis.chatHarness;
                export const chatApi = { send: (...args) => h.send(...args), stop: (id) => h.stops.push(id), approve: (...args) => h.approve(...args) };
                export const contextApi = {
                    clipboard: () => h.clipboard(),
                    autoClipboard: () => h.autoClipboard ? h.autoClipboard() : Promise.resolve(h.clipboard()).then((attachment) => ({ attachment, message: "" })),
                    screenshot: () => h.screenshot(), cancelScreenshot: async () => { h.screenshotCancels++; },
                    selectionStatus: () => h.selectionStatus(), selection: () => h.selection(),
                };
                export const historyApi = { recent: async () => h.recent, get: async (id) => h.detail(id) };
                export const popupApi = { hide: async () => { h.hides++; } };
                export const prefsApi = { get: async () => ({ attachClipboard: true }), hasKey: async () => true };
                export const errorText = (e) => e.message ?? String(e);
                export const events = Object.fromEntries(["open", "focus", "hidden", "delta", "done", "error", "tool", "approval"].map((name) => [name, (fn) => { h.handlers[name] = fn; }]));
            ` };
        }
        if (new URL(url).pathname.endsWith("state.svelte.ts")) {
            const source = ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText;
            return { format: "module", shortCircuit: true, source: compileModule(source, { filename: new URL(url).pathname }).js.code };
        }
        return next(url, context);
    },
});

const h = globalThis.chatHarness = {
    handlers: {}, stops: [], hides: 0, recent: [],
    clipboard: async () => ({ kind: "clipboard", label: "Clipboard", content: "copied" }),
};
let instance = 0;
async function fresh() {
    h.handlers = {}; h.stops = []; h.hides = 0; h.recent = [];
    h.approvalCalls = []; h.approve = async (...args) => { h.approvalCalls.push(args); };
    h.screenshotCancels = 0; h.screenshot = async () => null;
    h.selectionStatus = async () => ({ available: true, beforeFocus: false, message: "Reads after focus" });
    h.selection = async () => ({ attachment: null, message: "No selected text" });
    h.clipboard = async () => ({ kind: "clipboard", label: "Clipboard", content: "copied" });
    h.autoClipboard = null;
    const { app } = await import(`../src/lib/state.svelte.ts?test=${instance++}`);
    await app.init();
    return app;
}
const settle = () => new Promise((resolve) => setImmediate(resolve));

test("Web defaults on and preserves the user's selection across sends and new chats", async () => {
    const app = await fresh();
    const requests = [];
    h.send = async (req) => {
        requests.push(req);
        return { chatId: "web-chat", messageId: `web-${requests.length}`, title: "Web question" };
    };
    assert.equal(app.web, true);
    app.input = "Search for current facts";
    await app.send();
    assert.equal(requests[0].web, true);
    assert.equal(app.web, true);
    assert.equal(app.searchingWeb, true);
    app.toggleWeb();
    assert.equal(app.web, false);
    assert.equal(app.searchingWeb, true);
    h.handlers.delta({ chatId: "web-chat", messageId: "web-1", text: "Found an answer" });
    assert.equal(app.searchingWeb, false);
    h.handlers.done({ chatId: "web-chat", messageId: "web-1", citations: [{ url: "https://go.dev/", title: "Go" }], searches: 1 });
    assert.equal(app.messages[1].citations[0].title, "Go");
    assert.equal(app.messages[1].searches, 1);
    assert.equal(app.web, false);
    app.input = "Continue without searching";
    await app.send();
    assert.equal(requests[1].web, false);
    assert.equal(app.answerWeb, false);
    assert.equal(app.searchingWeb, false);
    app.newChat();
    assert.equal(app.web, false);
    assert.equal(app.answerWeb, false);
    app.toggleWeb();
    app.newChat();
    assert.equal(app.web, true);
});

test("blocked sends and request failures preserve the user's Web selection", async () => {
    const app = await fresh();
    await app.send();
    assert.equal(app.web, true);
    app.input = "Question";
    app.screenshotPending = true;
    await app.send();
    assert.equal(app.web, true);
    app.screenshotPending = false;
    app.hasKey = false;
    await app.send();
    assert.equal(app.web, true);
    app.hasKey = true;
    h.send = async () => { throw new Error("Rejected by provider"); };
    await app.send();
    assert.equal(app.input, "Question");
    assert.equal(app.web, true);
    assert.equal(app.searchingWeb, false);
    assert.equal(app.streaming, false);
    assert.equal(app.messages.length, 0);
    app.toggleWeb();
    await app.send();
    assert.equal(app.web, false);
    assert.equal(app.input, "Question");
});

test("early completion and failure preserve Web citations and distinguish unknown usage from zero", async () => {
    for (const event of ["done", "error"]) {
        const app = await fresh();
        h.send = async () => {
            h.handlers.delta({ chatId: "chat", messageId: "answer", text: "Answer with a source" });
            h.handlers[event]({ chatId: "chat", messageId: "answer", citations: [{ url: "https://go.dev/", title: "Go" }],
                ...(event === "done" ? { searches: 0, stopped: true } : { error: "Provider failed" }) });
            return { chatId: "chat", messageId: "answer", title: "Question" };
        };
        app.input = "Question";
        await app.send();
        assert.equal(app.messages[1].citations.length, 1);
        assert.equal(app.messages[1].searches, event === "done" ? 0 : undefined);
        assert.equal(app.messages[1].content, "Answer with a source");
        assert.equal(app.searchingWeb, false);
        assert.equal(app.streaming, false);
        assert.equal(app.web, true);
    }
});

test("hiding and History continuation preserve a pending search and its sources", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "web", messageId: "answer", title: "Question" });
    app.input = "Search";
    await app.send();
    app.escape();
    h.handlers.hidden();
    h.handlers.open({ mode: "bar" });
    assert.equal(app.view, "chat");
    assert.equal(app.searchingWeb, true);
    app.goTo("history");
    await app.openChat("web");
    assert.equal(app.searchingWeb, true);
    app.stop();
    assert.deepEqual(h.stops, ["web"]);
    h.handlers.done({ chatId: "web", messageId: "answer", stopped: true, citations: [{ url: "https://go.dev/", title: "Go" }] });
    app.goTo("history");
    await app.openChat("web");
    assert.equal(app.messages[1].stopped, true);
    assert.equal(app.messages[1].citations[0].url, "https://go.dev/");
    assert.equal(app.messages[1].searches, undefined);
});

test("stored source metadata loads without changing the user's Web selection", async () => {
    const app = await fresh();
    h.detail = async () => ({ chat: { id: "stored", title: "Stored" }, messages: [
        { id: "answer", role: "assistant", content: "A stored answer", citations: [{ url: "https://go.dev/", title: "Go" }], searches: 2 },
    ] });
    app.toggleWeb();
    await app.openChat("stored");
    assert.equal(app.messages[0].citations[0].title, "Go");
    assert.equal(app.messages[0].searches, 2);
    assert.equal(app.web, false);
});

function deferred() {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}

test("opening after native focus captures clipboard; dismissal and auto-attach off stick", async () => {
    const app = await fresh();
    h.handlers.focus();
    h.handlers.open({ mode: "bar" });
    await settle();
    assert.equal(app.attachments.length, 1);
    app.removeAttachment(0);
    h.handlers.open({ mode: "bar" });
    h.handlers.focus();
    await settle();
    assert.equal(app.attachments.length, 0);
    app.toggleAutoAttach();
    app.toggleAutoAttach();
    await settle();
    assert.equal(app.attachments.length, 1);
    app.toggleAutoAttach();
    assert.equal(app.attachments.length, 0);
});

test("turning auto-attach off invalidates a pending clipboard read", async () => {
    const app = await fresh();
    let finish;
    h.clipboard = () => new Promise((resolve) => { finish = resolve; });
    const capture = app.captureClipboard();
    app.toggleAutoAttach();
    finish({ kind: "clipboard", label: "Clipboard", content: "copied" });
    await capture;
    assert.equal(app.attachments.length, 0);
});

test("auto-attach holds back a secret-looking clipboard; the Clipboard button still attaches it", async () => {
    const app = await fresh();
    h.autoClipboard = async () => ({ attachment: null, message: "The clipboard looks like a password or key" });
    h.handlers.focus();
    h.handlers.open({ mode: "bar" });
    await settle();
    assert.equal(app.attachments.length, 0);
    assert.match(app.clipboardMessage, /looks like a password/);
    await app.captureClipboard(true);
    assert.deepEqual(app.attachments.map((a) => a.content), ["copied"]);
    assert.equal(app.clipboardMessage, "");
});

test("the Clipboard button attaches again after its chip was removed", async () => {
    const app = await fresh();
    h.handlers.focus();
    h.handlers.open({ mode: "bar" });
    await settle();
    app.removeAttachment(0);
    await app.captureClipboard(true);
    assert.equal(app.attachments.length, 1);
});

test("a held-back clipboard message is dropped when stale or on a new chat", async () => {
    const app = await fresh();
    let finish;
    h.autoClipboard = () => new Promise((resolve) => { finish = resolve; });
    const capture = app.captureClipboard();
    app.toggleAutoAttach();
    finish({ attachment: null, message: "held back" });
    await capture;
    assert.equal(app.clipboardMessage, "");
    app.toggleAutoAttach();
    finish({ attachment: null, message: "held back" });
    await settle();
    assert.equal(app.clipboardMessage, "held back");
    // Reopening resets the chat before focus allows a new clipboard read.
    h.handlers.open({ mode: "bar" });
    assert.equal(app.clipboardMessage, "");
});

test("opening before native focus waits to read the clipboard", async () => {
    const app = await fresh();
    h.handlers.open({ mode: "bar" });
    await settle();
    assert.equal(app.attachments.length, 0);
    h.handlers.focus();
    await settle();
    assert.equal(app.attachments.length, 1);
});

test("completion before Send returns keeps text and Stopped state", async () => {
    const app = await fresh();
    h.send = async () => {
        h.handlers.delta({ chatId: "chat", messageId: "answer", text: "Partial answer" });
        h.handlers.done({ chatId: "chat", messageId: "answer", model: "model", fallback: false, stopped: true });
        return { chatId: "chat", messageId: "answer", title: "Question" };
    };
    app.input = "Question";
    await app.send();
    assert.equal(app.messages[1].content, "Partial answer");
    assert.equal(app.messages[1].stopped, true);
    assert.equal(app.streaming, false);
});

test("hide and reopen preserve stream; follow-up reuses chat; new chat resets it", async () => {
    const app = await fresh();
    const requests = [];
    h.send = async (req) => {
        requests.push(req);
        return { chatId: "chat", messageId: `answer-${requests.length}`, title: "Question" };
    };
    app.input = "Question";
    await app.send();
    app.escape();
    assert.equal(h.hides, 1);
    h.handlers.hidden();
    h.handlers.open({ mode: "bar" });
    assert.equal(app.view, "chat");
    assert.equal(app.streaming, true);
    app.stop();
    assert.deepEqual(h.stops, ["chat"]);
    h.handlers.done({ chatId: "chat", messageId: "answer-1", stopped: true });
    app.input = "Follow-up";
    await app.send();
    assert.equal(requests[1].chatId, "chat");
    app.newChat();
    assert.equal(app.chatId, "");
    assert.equal(app.messages.length, 0);
    assert.equal(app.view, "bar");
});

test("starting a new chat while Send is pending does not restore the old chat", async () => {
    const app = await fresh();
    let finish;
    h.send = () => new Promise((resolve) => { finish = resolve; });
    app.input = "Old question";
    const sending = app.send();
    app.newChat();
    finish({ chatId: "old", messageId: "old-answer", title: "Old question" });
    await sending;
    assert.equal(app.chatId, "");
    assert.equal(app.messages.length, 0);
    assert.equal(app.view, "bar");
});

test("Stop before Send returns cancels once the chat ID is known", async () => {
    const app = await fresh();
    let finish;
    h.send = () => new Promise((resolve) => { finish = resolve; });
    app.input = "Question";
    const sending = app.send();
    app.stop();
    finish({ chatId: "chat", messageId: "answer", title: "Question" });
    await sending;
    assert.deepEqual(h.stops, ["chat"]);
});

test("recent chats refresh after completion and open with saved content", async () => {
    const app = await fresh();
    h.recent = [{ id: "saved", title: "Saved chat", updatedAt: new Date().toISOString() }];
    h.detail = async () => ({ chat: h.recent[0], messages: [
        { id: "user", role: "user", content: "Question" },
        { id: "answer", role: "assistant", content: "```go\nfmt.Println(1)\n```" },
    ] });
    await app.refreshRecent();
    assert.equal(app.recent[0].id, "saved");
    await app.openChat(app.recent[0].id);
    assert.equal(app.view, "chat");
    assert.equal(app.chatId, "saved");
    assert.match(app.messages[1].content, /fmt.Println/);
});

test("continuing the current chat from history keeps its live answer, draft, and Stop control", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "active", messageId: "answer", title: "Question" });
    h.detail = async () => ({ chat: { id: "active", title: "Question" }, messages: [
        { id: "user", role: "user", content: "Question" },
    ] });
    app.input = "Question";
    await app.send();
    h.handlers.delta({ chatId: "active", messageId: "answer", text: "First part. " });
    app.input = "Follow-up draft";
    app.attachments = [{ kind: "clipboard", label: "Clipboard", content: "draft context" }];
    app.modelOverride = "vendor/model";
    app.goTo("history");
    await app.openChat("active");

    assert.equal(app.view, "chat");
    assert.equal(app.streaming, true);
    assert.equal(app.input, "Follow-up draft");
    assert.equal(app.attachments[0]?.content, "draft context");
    assert.equal(app.modelOverride, "vendor/model");
    h.handlers.delta({ chatId: "active", messageId: "answer", text: "Second part." });
    assert.equal(app.messages[1]?.content, "First part. Second part.");
    app.stop();
    assert.deepEqual(h.stops, ["active"]);
    h.handlers.done({ chatId: "active", messageId: "answer", model: "vendor/model", fallback: false, stopped: true });
    assert.equal(app.streaming, false);
    assert.equal(app.messages[1]?.streaming, false);
    assert.equal(app.messages[1]?.stopped, true);
    assert.equal(app.messages[1]?.content, "First part. Second part.");
});

test("continuing the current chat preserves completion or error received while history is open", async () => {
    for (const terminal of [
        { event: "done", data: { model: "vendor/fallback", fallback: true, stopped: true } },
        { event: "error", data: { error: "Provider disconnected" } },
    ]) {
        const app = await fresh();
        h.send = async () => ({ chatId: "active", messageId: "answer", title: "Question" });
        h.detail = async () => ({ chat: { id: "active", title: "Question" }, messages: [
            { id: "user", role: "user", content: "Question" },
        ] });
        app.input = "Question";
        await app.send();
        h.handlers.delta({ chatId: "active", messageId: "answer", text: "Partial answer" });
        app.goTo("history");
        h.handlers[terminal.event]({ chatId: "active", messageId: "answer", ...terminal.data });
        await app.openChat("active");
        assert.equal(app.streaming, false);
        assert.equal(app.messages[1]?.content, "Partial answer");
        for (const [key, value] of Object.entries(terminal.data)) {
            assert.equal(app.messages[1]?.[key], value);
        }
    }
});

test("a late history load cannot overwrite a new question or its context", async () => {
    const app = await fresh();
    app.applySettings({ attachClipboard: false });
    const detail = deferred();
    h.detail = () => detail.promise;
    const opening = app.openChat("old");
    app.newChat();
    app.input = "New draft";
    app.attachments = [{ kind: "selection", label: "Selection", content: "new context" }];
    app.modelOverride = "vendor/new-model";
    detail.resolve({ chat: { id: "old", title: "Old chat" }, messages: [] });
    await opening;
    assert.equal(app.chatId, "");
    assert.equal(app.view, "bar");
    assert.equal(app.input, "New draft");
    assert.equal(app.attachments[0]?.content, "new context");
    assert.equal(app.modelOverride, "vendor/new-model");
});

test("the most recently requested chat wins when history loads finish out of order", async () => {
    const app = await fresh();
    const old = deferred(), latest = deferred();
    h.detail = (id) => id === "old" ? old.promise : latest.promise;
    const first = app.openChat("old");
    const second = app.openChat("latest");
    latest.resolve({ chat: { id: "latest", title: "Latest chat" }, messages: [
        { id: "latest-answer", role: "assistant", content: "Latest answer" },
    ] });
    await second;
    old.resolve({ chat: { id: "old", title: "Old chat" }, messages: [] });
    await first;
    assert.equal(app.chatId, "latest");
    assert.equal(app.chatTitle, "Latest chat");
    assert.equal(app.messages[0]?.content, "Latest answer");
});

test("an obsolete history failure cannot replace a newer chat's error", async () => {
    const app = await fresh();
    const old = deferred();
    h.detail = (id) => id === "old" ? old.promise : Promise.resolve({ chat: { id, title: "Latest chat" }, messages: [] });
    const first = app.openChat("old");
    await app.openChat("latest");
    app.error = "Current chat feedback";
    old.reject(new Error("Old chat failed"));
    await first;
    assert.equal(app.chatId, "latest");
    assert.equal(app.error, "Current chat feedback");
});

test("navigation and popup dismissal ignore late history results and errors", async () => {
    const actions = [
        ["Back", (app) => app.back()],
        ["Escape", (app) => app.escape()],
        ["Preferences", (app) => app.goTo("settings")],
        ["hide", (app) => app.hide()],
        ["native hide", () => h.handlers.hidden()],
        ["native Preferences", () => h.handlers.open({ mode: "settings" })],
        ["native reopen", () => h.handlers.open({ mode: "bar" })],
    ];
    for (const [name, navigate] of actions) {
        for (const fails of [false, true]) {
            const app = await fresh();
            app.goTo("history");
            const detail = deferred();
            h.detail = () => detail.promise;
            const opening = app.openChat("old");
            navigate(app);
            const view = app.view;
            app.input = "Current draft";
            app.error = "Current view feedback";
            if (fails) detail.reject(new Error("Obsolete failure"));
            else detail.resolve({ chat: { id: "old", title: "Old chat" }, messages: [] });
            await opening;
            assert.equal(app.view, view, name);
            assert.equal(app.chatId, "", name);
            assert.equal(app.input, "Current draft", name);
            assert.equal(app.error, "Current view feedback", name);
        }
    }
});

test("sending a new question supersedes an older pending history load", async () => {
    const app = await fresh();
    const detail = deferred();
    h.detail = () => detail.promise;
    const opening = app.openChat("old");
    h.send = async () => ({ chatId: "new", messageId: "new-answer", title: "New question" });
    app.input = "New question";
    await app.send();
    detail.resolve({ chat: { id: "old", title: "Old chat" }, messages: [] });
    await opening;
    assert.equal(app.chatId, "new");
    assert.equal(app.streaming, true);
    assert.equal(app.messages[0]?.content, "New question");
    assert.equal(app.messages[1]?.id, "new-answer");
});

test("returning to the current live chat supersedes an older pending history load", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "active", messageId: "answer", title: "Question" });
    app.input = "Question";
    await app.send();
    app.goTo("history");
    const detail = deferred();
    h.detail = (id) => id === "old" ? detail.promise : Promise.resolve({ chat: { id, title: "Question" }, messages: [] });
    const opening = app.openChat("old");
    await app.openChat("active");
    detail.resolve({ chat: { id: "old", title: "Old chat" }, messages: [] });
    await opening;
    assert.equal(app.chatId, "active");
    assert.equal(app.streaming, true);
    assert.equal(app.messages[1]?.id, "answer");
});

test("a current history load failure stays visible and retry can open the chat", async () => {
    const app = await fresh();
    app.goTo("history");
    h.detail = async () => { throw new Error("Chat is unavailable"); };
    await app.openChat("saved");
    assert.equal(app.view, "history");
    assert.equal(app.error, "Chat is unavailable");
    h.detail = async () => ({ chat: { id: "saved", title: "Saved chat" }, messages: [] });
    await app.openChat("saved");
    assert.equal(app.chatId, "saved");
    assert.equal(app.view, "chat");
    assert.equal(app.error, "");
});

test("deleting the current chat clears resume state and refreshes recent chats", async () => {
    const app = await fresh();
    app.chatId = "deleted";
    app.chatTitle = "Deleted chat";
    app.messages = [{ id: "old", role: "user", content: "Question" }];
    app.view = "history";
    app.returnTo = "chat";
    h.recent = [{ id: "remaining", title: "Remaining chat" }];
    await app.forgetDeletedChat("deleted");
    assert.equal(app.chatId, "");
    assert.equal(app.messages.length, 0);
    assert.equal(app.view, "history");
    assert.equal(app.returnTo, "bar");
    assert.equal(app.recent[0].id, "remaining");
});

test("saved clipboard preference drops its chip and invalidates pending capture", async () => {
    const app = await fresh();
    let finish;
    h.clipboard = () => new Promise((resolve) => { finish = resolve; });
    const capture = app.captureClipboard();
    app.applySettings({ attachClipboard: false });
    finish({ kind: "clipboard", label: "Clipboard", content: "copied" });
    await capture;
    assert.equal(app.autoAttach, false);
    assert.equal(app.attachments.length, 0);
});

test("clearing history resets chat state while leaving preferences open", async () => {
    const app = await fresh();
    app.chatId = "old";
    app.messages = [{ id: "message", role: "user", content: "Question" }];
    app.view = "settings";
    app.returnTo = "chat";
    await app.historyCleared();
    assert.equal(app.chatId, "");
    assert.equal(app.messages.length, 0);
    assert.equal(app.view, "settings");
    assert.equal(app.returnTo, "bar");
    assert.equal(app.recent.length, 0);
});

const screenshot = { kind: "screenshot", label: "Screenshot · 640 × 480", content: "data:image/png;base64,image" };

test("interactive screenshot preserves draft across native hide/focus and attaches only once", async () => {
    const app = await fresh();
    app.applySettings({ attachClipboard: false, offerScreenshot: true });
    app.input = "What does this error mean?";
    let finish, calls = 0;
    h.screenshot = () => { calls++; return new Promise((resolve) => { finish = resolve; }); };
    const capture = app.addScreenshot();
    assert.equal(app.screenshotPending, true);
    await app.addScreenshot();
    await app.send();
    h.handlers.hidden();
    h.handlers.open({ mode: "bar" });
    h.handlers.focus();
    finish(screenshot);
    await capture;
    assert.equal(calls, 1);
    assert.equal(app.screenshotPending, false);
    assert.equal(app.input, "What does this error mean?");
    assert.equal(app.attachments[0].content, screenshot.content);
    await app.addScreenshot();
    assert.equal(calls, 1);
});

test("cancelled or failed screenshot leaves composer usable", async () => {
    const app = await fresh();
    app.applySettings({ attachClipboard: false, offerScreenshot: true });
    app.input = "Question";
    await app.addScreenshot();
    assert.equal(app.attachments.length, 0);
    assert.equal(app.error, "");
    h.screenshot = async () => { throw new Error("Permission denied"); };
    await app.addScreenshot();
    assert.equal(app.input, "Question");
    assert.equal(app.screenshotPending, false);
    assert.equal(app.error, "Permission denied");
});

test("removing screenshot excludes it from the request; sending keeps its history chip", async () => {
    const app = await fresh();
    app.applySettings({ attachClipboard: false, offerScreenshot: true });
    h.screenshot = async () => screenshot;
    const requests = [];
    h.send = async (req) => {
        requests.push(req);
        return { chatId: "chat", messageId: `answer-${requests.length}`, title: "Question" };
    };
    await app.addScreenshot();
    app.removeAttachment(0);
    app.input = "Question";
    await app.send();
    assert.equal(requests[0].attachments.length, 0);
    h.handlers.done({ chatId: "chat", messageId: "answer-1" });
    await app.addScreenshot();
    app.input = "With a screenshot";
    await app.send();
    assert.equal(requests[1].attachments[0].content, screenshot.content);
    assert.equal(app.messages[2].attachments[0].label, screenshot.label);
    assert.equal(app.attachments.length, 0);
});

test("new chat, hide, and disabling screenshots invalidate a pending capture", async () => {
    for (const action of ["newChat", "hide", "disable"]) {
        const app = await fresh();
        app.applySettings({ attachClipboard: false, offerScreenshot: true });
        let finish;
        h.screenshot = () => new Promise((resolve) => { finish = resolve; });
        const capture = app.addScreenshot();
        if (action === "disable") app.applySettings({ attachClipboard: false, offerScreenshot: false });
        else app[action]();
        assert.equal(h.screenshotCancels, 1);
        finish(screenshot);
        await capture;
        assert.equal(app.attachments.length, 0);
        assert.equal(app.screenshotPending, false);
    }
});

const selectedText = { kind: "selection", label: "Selection", content: "highlighted, not copied" };
const enableSelection = (app) => app.applySettings({ attachClipboard: false, attachSelection: true, offerScreenshot: true });

test("selected text waits for focus in either native event order and remains separate from clipboard", async () => {
    for (const focusFirst of [true, false]) {
        const app = await fresh();
        enableSelection(app);
        let reads = 0;
        h.selection = async () => { reads++; return { attachment: selectedText, message: "" }; };
        if (focusFirst) h.handlers.focus();
        h.handlers.open({ mode: "bar" });
        if (!focusFirst) {
            assert.equal(reads, 0);
            h.handlers.focus();
        }
        await settle();
        assert.equal(reads, 1);
        assert.equal(app.attachments.length, 1);
        assert.equal(app.attachments[0].content, selectedText.content);
        h.handlers.focus();
        await settle();
        assert.equal(reads, 1);
    }
});

test("X11 uses the selection captured before focus, including an empty capture", async () => {
    const app = await fresh();
    enableSelection(app);
    app.selectionSupport = { available: true, beforeFocus: true, message: "Reads before focus" };
    h.selection = async () => { throw new Error("must use the captured selection"); };
    h.handlers.open({ mode: "bar", selection: { attachment: selectedText, message: "" } });
    h.handlers.focus();
    await settle();
    assert.equal(app.attachments[0].content, selectedText.content);
    app.newChat();
    await settle();
    h.handlers.open({ mode: "bar", selection: { attachment: null, message: "No external selection" } });
    await settle();
    assert.equal(app.attachments.length, 0);
    assert.equal(app.selectionMessage, "No external selection");
});

test("removing selected text sticks across reopening, and manual retry or a new chat can capture again", async () => {
    const app = await fresh();
    enableSelection(app);
    let reads = 0;
    h.selection = async () => { reads++; return { attachment: selectedText, message: "" }; };
    h.handlers.open({ mode: "bar" }); h.handlers.focus();
    await settle();
    app.removeAttachment(0);
    h.handlers.hidden(); h.handlers.open({ mode: "bar" }); h.handlers.focus();
    await settle();
    assert.equal(reads, 1);
    assert.equal(app.attachments.length, 0);
    await app.captureSelection(true);
    assert.equal(app.attachments.length, 1);
    app.removeAttachment(0);
    app.newChat();
    await settle();
    assert.equal(app.attachments.length, 1);
});

test("selection disabling, hiding, and chat reset ignore late capture results", async () => {
    for (const action of ["disable", "hide", "newChat"]) {
        const app = await fresh();
        enableSelection(app);
        let finish;
        h.selection = () => new Promise((resolve) => { finish = resolve; });
        const reading = app.captureSelection();
        if (action === "disable") app.applySettings({ attachClipboard: false, attachSelection: false });
        else app[action]();
        finish({ attachment: selectedText, message: "" });
        await reading;
        assert.equal(app.attachments.length, 0);
        assert.equal(app.selectionPending, false);
    }
    const app = await fresh();
    enableSelection(app);
    app.attachments = [selectedText];
    app.applySettings({ attachClipboard: false, attachSelection: false });
    assert.equal(app.attachments.length, 0);
});

test("unavailable, empty, and failed selections do not read clipboard or prevent sending", async () => {
    for (const kind of ["unavailable", "empty", "failure"]) {
        const app = await fresh();
        enableSelection(app);
        let reads = 0;
        h.clipboard = async () => { throw new Error("clipboard must not substitute for selection"); };
        h.selection = async () => {
            reads++;
            if (kind === "failure") throw new Error("Source cleared its selection");
            return { attachment: null, message: "No selected text" };
        };
        if (kind === "unavailable") app.selectionSupport = { available: false, beforeFocus: false, message: "Unavailable here" };
        h.handlers.open({ mode: "bar" }); h.handlers.focus();
        await settle();
        assert.equal(reads, kind === "unavailable" ? 0 : 1);
        assert.equal(app.attachments.length, 0);
        assert.equal(app.error, "");
        assert.ok(app.selectionMessage);
        h.send = async (req) => {
            assert.equal(req.attachments.length, 0);
            return { chatId: "chat", messageId: "answer", title: "Question" };
        };
        app.input = "Question";
        await app.send();
        assert.equal(app.chatId, "chat");
    }
});

test("selected-text chips are sent once and removal excludes them", async () => {
    for (const remove of [true, false]) {
        const app = await fresh();
        enableSelection(app);
        h.selection = async () => ({ attachment: selectedText, message: "" });
        await app.captureSelection();
        if (remove) app.removeAttachment(0);
        h.send = async (req) => {
            assert.equal(req.attachments.length, remove ? 0 : 1);
            if (!remove) assert.equal(req.attachments[0].content, selectedText.content);
            return { chatId: "chat", messageId: "answer", title: "Question" };
        };
        app.input = "Question";
        await app.send();
        assert.equal(app.messages[0].attachments.length, remove ? 0 : 1);
        assert.equal(app.attachments.length, 0);
    }
});

const tool = (callId, status = "started", summary = "Reading todo.md") => ({ callId, name: "read_file", summary, status });

test("tool hints follow scoped activity and finished calls do not return to running", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "tools", messageId: "answer", title: "Question" });
    app.input = "Read my todo";
    await app.send();
    h.handlers.tool({ chatId: "wrong", messageId: "answer", activity: tool("wrong") });
    assert.equal(app.toolHint, "");
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read") });
    assert.equal(app.toolHint, "Reading todo.md…");
    assert.equal(app.messages[1].activity.length, 1);
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read", "finished", "Read todo.md") });
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read") });
    assert.equal(app.toolHint, "");
    assert.equal(app.messages[1].activity[0].summary, "Read todo.md");
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("second") });
    assert.equal(app.toolHint, "Reading todo.md…");
    h.handlers.done({ chatId: "tools", messageId: "answer", model: "glm/model", activity: [tool("read", "finished", "Read todo.md"), tool("second", "stopped", "Read other.md")], stopped: true });
    assert.equal(app.toolHint, "");
    assert.equal(app.messages[1].activity[1].status, "stopped");
    assert.equal(app.messages[1].stopped, true);
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("third") });
    h.handlers.delta({ chatId: "tools", messageId: "answer", text: "late text" });
    assert.equal(app.messages[1].activity.length, 2);
    assert.equal(app.messages[1].content, "");
});

test("early tool events and terminal snapshots survive Send returning later", async () => {
    for (const terminal of ["done", "error", "pending"]) {
        const app = await fresh();
        const activity = [tool("read", terminal === "error" ? "error" : "finished", "Read todo.md")];
        h.send = async () => {
            h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read") });
            h.handlers.tool({ chatId: "tools", messageId: "answer", activity: activity[0] });
            h.handlers.delta({ chatId: "tools", messageId: "answer", text: "Answer" });
            if (terminal !== "pending") h.handlers[terminal]({ chatId: "tools", messageId: "answer", model: "gpt/test", activity,
                ...(terminal === "error" ? { error: "Provider failed" } : { stopped: false }) });
            return { chatId: "tools", messageId: "answer", title: "Question" };
        };
        app.input = "Read";
        await app.send();
        assert.equal(app.messages[1].activity.length, 1);
        assert.equal(app.messages[1].activity[0].status, activity[0].status);
        assert.equal(app.messages[1].content, "Answer");
        assert.equal(app.streaming, terminal === "pending");
        if (terminal !== "pending") assert.equal(app.messages[1].model, "gpt/test");
    }
});

test("tool activity survives hide and current History navigation, and stops through the existing shortcut", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "tools", messageId: "answer", title: "Question" });
    app.input = "Read";
    await app.send();
    h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read") });
    app.escape();
    h.handlers.hidden();
    assert.deepEqual(h.stops, []);
    h.handlers.open({ mode: "bar" });
    assert.equal(app.view, "chat");
    assert.equal(app.toolHint, "Reading todo.md…");
    app.goTo("history");
    await app.openChat("tools");
    assert.equal(app.toolHint, "Reading todo.md…");
    app.stop();
    assert.deepEqual(h.stops, ["tools"]);
    h.handlers.done({ chatId: "tools", messageId: "answer", stopped: true, activity: [tool("read", "stopped", "Read todo.md")] });
    assert.equal(app.toolHint, "");
    assert.equal(app.messages[1].activity[0].status, "stopped");
});

test("new chats ignore old tool events and terminal events, including while a new Send is pending", async () => {
    const app = await fresh();
    h.send = async () => ({ chatId: "old", messageId: "old-answer", title: "Old" });
    app.input = "First";
    await app.send();
    app.newChat();
    h.handlers.tool({ chatId: "old", messageId: "old-answer", activity: tool("old-call") });
    assert.equal(app.toolHint, "");
    h.send = async () => {
        h.handlers.tool({ chatId: "old", messageId: "old-answer", activity: tool("old-call") });
        h.handlers.done({ chatId: "old", messageId: "old-answer", activity: [tool("old-call", "finished")] });
        h.handlers.tool({ chatId: "new", messageId: "new-answer", activity: tool("new-call") });
        return { chatId: "new", messageId: "new-answer", title: "New" };
    };
    app.input = "Second";
    await app.send();
    assert.equal(app.streaming, true);
    assert.deepEqual(app.messages[1].activity.map((a) => a.callId), ["new-call"]);
    assert.equal(app.toolHint, "Reading todo.md…");
});

test("History restores saved activity, stopped status and errors", async () => {
    const app = await fresh();
    h.detail = async () => ({ chat: { id: "saved", title: "Saved" }, messages: [
        { id: "answer", role: "assistant", content: "Partial", stopped: true, error: "Loop limit", activity: [tool("read", "finished", "Read todo.md")] },
    ] });
    await app.openChat("saved");
    assert.equal(app.messages[0].activity[0].summary, "Read todo.md");
    assert.equal(app.messages[0].stopped, true);
    assert.equal(app.messages[0].error, "Loop limit");
    assert.equal(app.toolHint, "");
});

test("failed sends discard early activity and keep the draft for retry", async () => {
    const app = await fresh();
    h.send = async () => {
        h.handlers.tool({ chatId: "tools", messageId: "answer", activity: tool("read") });
        throw new Error("Send failed");
    };
    app.input = "Read";
    await app.send();
    assert.equal(app.input, "Read");
    assert.equal(app.toolHint, "");
    h.send = async () => ({ chatId: "tools", messageId: "answer", title: "Retry" });
    await app.send();
    assert.equal(app.messages[1].activity, undefined);
    assert.equal(app.toolHint, "");
});

const approval = (overrides = {}) => ({ chatId: "shell", messageId: "answer", callId: "call", command: "df -h", workingDirectory: "/home/test", expiresAt: new Date(Date.now()+120000).toISOString(), state: "pending", ...overrides });
async function shellAnswer() {
    const app = await fresh();
    h.send = async () => ({ chatId: "shell", messageId: "answer", title: "Disk space" });
    app.input = "How much disk space?";
    await app.send();
    return app;
}

test("approvals expose the exact command and send only the scoped decision", async () => {
    const app = await shellAnswer();
    const event = approval({ command: "  printf 'first'\n\tprintf 'second'  " });
    h.handlers.approval(event);
    assert.equal(app.approval.command, event.command);
    assert.equal(app.approval.workingDirectory, "/home/test");
    await app.answerApproval(true);
    assert.deepEqual(h.approvalCalls, [["shell", "answer", "call", true]]);
    h.handlers.approval({ ...event, state: "closed", outcome: "allowed" });
    assert.equal(app.approval, undefined);
    h.handlers.approval(event);
    assert.equal(app.approval, undefined);
});

test("double approval clicks cannot issue two pending decisions", async () => {
    const app = await shellAnswer();
    h.handlers.approval(approval());
    let resolve;
    h.approve = (...args) => { h.approvalCalls.push(args); return new Promise((yes) => { resolve = yes; }); };
    const pending = app.answerApproval(true);
    assert.equal(app.approvalBusy, true);
    await app.answerApproval(false);
    assert.equal(h.approvalCalls.length, 1);
    h.handlers.approval(approval({ state: "closed", outcome: "allowed" }));
    resolve(); await pending;
    assert.equal(app.approvalBusy, false);
});

test("Esc denies a pending command and keeps the chat open, then returns to ordinary hide", async () => {
    const app = await shellAnswer();
    h.handlers.approval(approval());
    app.escape(); await settle();
    assert.deepEqual(h.approvalCalls, [["shell", "answer", "call", false]]);
    assert.equal(h.hides, 0);
    assert.equal(app.streaming, true);
    h.handlers.approval(approval({ state: "closed", outcome: "denied" }));
    app.escape();
    assert.equal(h.hides, 1);
});

test("early approvals wait for Send identity and already-closed approvals never reopen", async () => {
    for (const closed of [false, true]) {
        const app = await fresh();
        h.send = async () => {
            h.handlers.approval(approval());
            if (closed) h.handlers.approval(approval({ state: "closed", outcome: "expired" }));
            return { chatId: "shell", messageId: "answer", title: "Disk" };
        };
        app.input = "Question";
        await app.send();
        assert.equal(Boolean(app.approval), !closed);
        assert.equal(h.approvalCalls.length, 0);
    }
});

test("wrong-chat and completed-answer approvals are denied without showing a card", async () => {
    const app = await shellAnswer();
    h.handlers.approval(approval({ chatId: "wrong" }));
    assert.equal(app.approval, undefined);
    h.handlers.done({ chatId: "shell", messageId: "answer", stopped: false });
    h.handlers.approval(approval());
    await settle();
    assert.deepEqual(h.approvalCalls, [["wrong", "answer", "call", false], ["shell", "answer", "call", false]]);
    assert.equal(app.approval, undefined);
});

test("new chats deny pending and late commands without transferring approval to the next answer", async () => {
    const app = await shellAnswer();
    h.handlers.approval(approval());
    app.newChat(); await settle();
    assert.equal(app.approval, undefined);
    assert.deepEqual(h.approvalCalls, [["shell", "answer", "call", false]]);
    h.handlers.approval(approval({ callId: "late" })); await settle();
    assert.equal(h.approvalCalls.at(-1)[2], "late");
    h.send = async () => {
        h.handlers.approval(approval({ callId: "old" }));
        h.handlers.approval(approval({ chatId: "new", messageId: "new-answer", callId: "new-call" }));
        return { chatId: "new", messageId: "new-answer", title: "New" };
    };
    app.input = "New question"; await app.send(); await settle();
    assert.equal(app.approval.callId, "new-call");
    assert.equal(h.approvalCalls.at(-1)[2], "old");
});

test("side-view navigation retains the current command and Esc steps back before denying", async () => {
    const app = await shellAnswer(); h.handlers.approval(approval());
    app.goTo("history"); app.escape();
    assert.equal(app.view, "chat"); assert.equal(h.approvalCalls.length, 0);
    assert.equal(app.approval.callId, "call");
    app.escape(); await settle();
    assert.equal(h.approvalCalls.at(-1)[3], false);
});

test("revoking local or shell permission clears pending approval and never allows it", async () => {
    for (const permission of ["localTools", "allowShellCommands"]) {
        const app = await shellAnswer(); h.handlers.approval(approval());
        app.applySettings({ attachClipboard: false, localTools: true, allowShellCommands: true, [permission]: false });
        await settle();
        assert.equal(app.approval, undefined);
        assert.deepEqual(h.approvalCalls, [["shell", "answer", "call", false]]);
    }
});

test("approval RPC errors can be retried and old errors cannot affect a newer request", async () => {
    const app = await shellAnswer(); h.handlers.approval(approval());
    h.approve = async () => { throw new Error("Connection interrupted"); };
    await app.answerApproval(true);
    assert.match(app.approvalError, /Connection interrupted/);
    assert.equal(app.approvalBusy, false);
    let reject;
    h.approve = () => new Promise((_, no) => { reject = no; });
    const pending = app.answerApproval(true);
    h.handlers.approval(approval({ state: "closed", outcome: "expired" }));
    h.handlers.approval(approval({ callId: "new-call" }));
    reject(new Error("Old error")); await pending;
    assert.equal(app.approvalError, ""); assert.equal(app.approval.callId, "new-call");
});

test("Stop and terminal events remove approval and settle waiting activity", async () => {
    const app = await shellAnswer(); h.handlers.approval(approval());
    h.handlers.tool({ chatId: "shell", messageId: "answer", activity: { callId: "call", name: "run_command", summary: "Waiting for command approval", status: "waiting" } });
    app.stop(); assert.deepEqual(h.stops, ["shell"]);
    h.handlers.done({ chatId: "shell", messageId: "answer", stopped: true }); await settle();
    assert.equal(app.approval, undefined); assert.equal(app.messages[1].activity[0].status, "stopped");
    assert.equal(app.toolHint, "");
});

test("failed sends decline early pending approvals and restore the question", async () => {
    const app = await fresh();
    h.send = async () => { h.handlers.approval(approval()); throw new Error("Send failed"); };
    app.input = "Question"; await app.send(); await settle();
    assert.equal(app.input, "Question"); assert.equal(app.approval, undefined);
    assert.deepEqual(h.approvalCalls, [["shell", "answer", "call", false]]);
});
