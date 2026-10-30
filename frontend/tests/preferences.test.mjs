import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import ts from "typescript";
import { compileModule } from "svelte/compiler";

registerHooks({
    resolve(specifier, context, next) {
        if (specifier === "./providers") return { url: new URL("../src/lib/providers.ts", import.meta.url).href, shortCircuit: true };
        return next(specifier, context);
    },
    load(url, context, next) {
        if (new URL(url).pathname.endsWith("preferences.svelte.ts")) {
            const source = ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText;
            return { format: "module", shortCircuit: true, source: compileModule(source, { filename: new URL(url).pathname }).js.code };
        }
        return next(url, context);
    },
});
const { Preferences } = await import("../src/lib/preferences.svelte.ts");
const settings = () => ({
    defaultModel: "test/model", fallbacks: ["other/model"], systemPrompt: "Be concise",
    attachClipboard: true, attachSelection: false, offerScreenshot: true, maxContextChars: 20000,
    voice: { engine: "whisper-server", whisperUrl: "http://127.0.0.1:8178", whisperServerBin: "", whisperModelPath: "", language: "en" },
    keepChatsDays: 0, usePortalShortcuts: false,
    webSearch: { maxResults: 4, maxFetches: 2, maxToolCalls: 5 },
});
const settle = () => new Promise((resolve) => setImmediate(resolve));
function deferred() {
    let resolve;
    const promise = new Promise((yes) => { resolve = yes; });
    return { promise, resolve };
}
async function setup(t, overrides = {}) {
    const calls = { saved: [], applied: [], keys: [], clear: 0, cleared: 0 };
    const prefs = new Preferences({
        get: async () => settings(),
        save: async (s) => { calls.saved.push(s); },
        saved: (s) => { calls.applied.push(s); },
        models: async () => [{ id: "test/model", name: "Test model", contextLength: 8192 }],
        hasKey: async () => true,
        setKey: async (key) => { calls.keys.push(key); },
        keyChanged: () => {},
        clearHistory: async () => { calls.clear++; },
        cleared: async () => { calls.cleared++; },
        canClear: () => true,
        ...overrides,
    });
    t.after(() => prefs.dispose());
    await prefs.load();
    return { prefs, calls };
}

test("preferences load all fields and normalize zero fallbacks", async (t) => {
    const { prefs } = await setup(t, { get: async () => ({ ...settings(), fallbacks: null }) });
    assert.equal(prefs.loading, false);
    assert.deepEqual([...prefs.settings.fallbacks], []);
    assert.equal(prefs.settings.voice.language, "en");
    assert.equal(prefs.settings.keepChatsDays, 0);
});

test("provider connections survive editing unrelated preferences", async (t) => {
    const ollama = { id: "c-0123456789ab", kind: "openai-compatible", name: "Ollama", baseUrl: "http://localhost:11434/v1", defaultModel: "llama3.3", fallbacks: null, contextTokens: 8192, maxTokens: 0 };
    const { prefs, calls } = await setup(t, { get: async () => ({ ...settings(), provider: "c-0123456789ab", connections: [ollama] }) });
    assert.deepEqual([...prefs.settings.connections[0].fallbacks], []);
    prefs.settings.systemPrompt = "Edited";
    prefs.changed(0);
    await prefs.flush();
    const saved = calls.saved.at(-1);
    assert.equal(saved.provider, "c-0123456789ab");
    assert.deepEqual(saved.connections, [{ ...ollama, fallbacks: [] }]);

    const legacy = await setup(t);
    assert.equal(legacy.prefs.settings.provider, "");
    assert.deepEqual([...legacy.prefs.settings.connections], []);
});

const ollamaPreset = { id: "", kind: "openai-compatible", name: "Ollama", baseUrl: "http://localhost:11434/v1", defaultModel: "", fallbacks: null, contextTokens: 4096, maxTokens: 0 };

// A backend that owns connection IDs and refuses saves whose list changed.
function connectionBackend() {
    const backend = { stored: { ...settings(), provider: "", connections: [] }, keys: {}, saves: [], adds: 0, removes: [], keyed: [], rejected: 0 };
    const sameIds = (a, b) => a.length === b.length && a.every((c) => b.some((d) => d.id === c.id && d.kind === c.kind));
    backend.actions = {
        get: async () => structuredClone(backend.stored),
        save: async (s) => {
            if (!sameIds(s.connections, backend.stored.connections)) { backend.rejected++; throw new Error("providers changed since Preferences opened"); }
            backend.saves.push(structuredClone(s));
            backend.stored = structuredClone(s);
        },
        presets: async () => [ollamaPreset],
        addConnection: async (c) => {
            backend.adds++;
            backend.stored.connections.push({ ...c, id: `c-00000000000${backend.adds}` });
            return structuredClone(backend.stored);
        },
        removeConnection: async (id) => {
            backend.removes.push(id);
            backend.stored.connections = backend.stored.connections.filter((c) => c.id !== id);
            return structuredClone(backend.stored);
        },
        hasConnectionKey: async (id) => Boolean(backend.keys[id]),
        setConnectionKey: async (id, key) => {
            const c = backend.stored.connections.find((c) => c.id === id);
            backend.keyed.push({ id, key, baseUrl: c.baseUrl });
            backend.keys[id] = key;
        },
    };
    return backend;
}

test("adding a provider selects it and its models replace OpenRouter's", async (t) => {
    const backend = connectionBackend();
    const { prefs } = await setup(t, backend.actions);
    assert.deepEqual(prefs.presets.map((p) => p.name), ["Ollama"]);
    prefs.settings.systemPrompt = "Unsaved edit";
    prefs.changed(10_000);
    await prefs.addConnection(prefs.presets[0]);
    assert.equal(backend.saves[0].systemPrompt, "Unsaved edit", "pending edits are saved before the add");
    const ollama = prefs.settings.connections[0];
    assert.equal(ollama.id, "c-000000000001");
    assert.equal(prefs.settings.provider, ollama.id);
    assert.equal(prefs.modelTarget, ollama);
    await prefs.flush();
    assert.equal(prefs.error, "Pick a default model.");
    prefs.modelTarget.defaultModel = " llama3.3 ";
    prefs.changed(0);
    await prefs.flush();
    const saved = backend.saves.at(-1);
    assert.equal(saved.provider, ollama.id);
    assert.equal(saved.connections[0].defaultModel, "llama3.3");
    assert.equal(saved.defaultModel, "test/model", "OpenRouter's model is kept");

    prefs.setProvider("openrouter");
    assert.equal(prefs.modelTarget, prefs.settings);
    await prefs.flush();
    assert.equal(backend.saves.at(-1).provider, "openrouter");
});

test("a direct provider suggests its own models and takes fallbacks", async (t) => {
    const backend = connectionBackend();
    const lists = [];
    backend.actions.connectionModels = (id) => { const d = deferred(); lists.push({ id, ...d }); return d.promise; };
    const { prefs } = await setup(t, backend.actions);
    await prefs.addConnection(ollamaPreset);
    const ollama = prefs.settings.connections[0];
    await settle();
    lists.at(-1).resolve([{ id: "llama3.3", name: "" }]);
    await settle();
    assert.equal(lists.at(-1).id, ollama.id);
    assert.deepEqual(prefs.connectionModels.map((m) => m.id), ["llama3.3"]);

    // Fallbacks belong to the provider, not OpenRouter.
    prefs.modelTarget.defaultModel = "llama3.3";
    assert.equal(prefs.addFallback("qwen3"), true);
    await prefs.flush();
    assert.deepEqual(backend.saves.at(-1).connections[0].fallbacks, ["qwen3"]);
    assert.deepEqual(backend.saves.at(-1).fallbacks, settings().fallbacks);

    // A moved address drops the list until it's saved and asked again.
    const asked = lists.length;
    prefs.active.baseUrl = "http://127.0.0.1:11434/v1";
    prefs.connectionChanged(10_000);
    assert.deepEqual(prefs.connectionModels, []);
    await prefs.flush();
    await settle();
    assert.ok(lists.length > asked, "asked again after the save");

    // An answer for a provider no longer shown is ignored.
    const late = lists.at(-1);
    prefs.setProvider("openrouter");
    late.resolve([{ id: "stale", name: "" }]);
    await settle();
    assert.deepEqual(prefs.connectionModels, []);

});

test("a provider that can't list its models leaves typing working", async (t) => {
    const backend = connectionBackend();
    backend.actions.connectionModels = async () => { throw new Error("Ollama isn't running"); };
    const { prefs } = await setup(t, backend.actions);
    await prefs.addConnection(ollamaPreset);
    await settle();
    assert.equal(prefs.connectionModelsError, "Ollama isn't running");
    assert.deepEqual(prefs.connectionModels, []);
    prefs.modelTarget.defaultModel = "llama3.3";
    prefs.changed(0);
    await prefs.flush();
    assert.equal(prefs.error, "");
    assert.equal(backend.saves.at(-1).connections[0].defaultModel, "llama3.3");
});

test("an edit made while a provider is being added saves with the new list", async (t) => {
    const backend = connectionBackend();
    let release;
    const add = backend.actions.addConnection;
    const { prefs } = await setup(t, { ...backend.actions, addConnection: (c) => { const result = add(c); return new Promise((resolve) => { release = () => resolve(result); }); } });
    const adding = prefs.addConnection(ollamaPreset);
    await settle();
    prefs.settings.systemPrompt = "Typed during the add";
    prefs.changed(0);
    await prefs.flush();
    release();
    await adding;
    await prefs.flush();
    prefs.modelTarget.defaultModel = "llama3.3";
    prefs.changed(0);
    await prefs.flush();
    assert.equal(prefs.error, "");
    const saved = backend.saves.at(-1);
    assert.equal(saved.systemPrompt, "Typed during the add");
    assert.equal(saved.connections.length, 1);
    assert.equal(backend.rejected, 0, "no save went out with the old list");
});

// Holds one backend action open until release() is called.
function held(action) {
    const gate = deferred();
    return { release: gate.resolve, action: async (...args) => { await gate.promise; return action(...args); } };
}

test("an edit to the active provider made while another is removed is kept", async (t) => {
    const backend = connectionBackend();
    const removal = held(backend.actions.removeConnection);
    const { prefs } = await setup(t, { ...backend.actions, removeConnection: removal.action });
    await prefs.addConnection(ollamaPreset);
    prefs.settings.connections[0].defaultModel = "llama3.3";
    prefs.changed(0);
    await prefs.addConnection(ollamaPreset);
    prefs.settings.connections[1].defaultModel = "qwen3";
    prefs.changed(0);
    const [first, second] = prefs.settings.connections;
    prefs.setProvider(first.id);
    await prefs.flush();
    const removing = prefs.removeConnection(second.id);
    await settle();
    prefs.active.defaultModel = "llama4";
    prefs.active.fallbacks.push("qwen3");
    prefs.changed(0);
    await prefs.flush();
    removal.release();
    await removing;
    await prefs.flush();
    assert.equal(prefs.error, "");
    assert.deepEqual(prefs.settings.connections.map((c) => [c.id, c.defaultModel, [...c.fallbacks]]), [[first.id, "llama4", ["qwen3"]]]);
    assert.equal(backend.saves.at(-1).connections[0].defaultModel, "llama4");
    assert.deepEqual(backend.saves.at(-1).connections[0].fallbacks, ["qwen3"]);
});

test("an edit to the active provider made while another is added is kept", async (t) => {
    const backend = connectionBackend();
    const addition = held(backend.actions.addConnection);
    const { prefs } = await setup(t, { ...backend.actions, addConnection: (c) => backend.adds ? addition.action(c) : backend.actions.addConnection(c) });
    await prefs.addConnection(ollamaPreset);
    prefs.settings.connections[0].defaultModel = "llama3.3";
    prefs.changed(0);
    await prefs.flush();
    const first = prefs.settings.connections[0];
    const adding = prefs.addConnection(ollamaPreset);
    await settle();
    prefs.active.contextTokens = 32768;
    prefs.changed(0);
    await prefs.flush();
    addition.release();
    await adding;
    assert.notEqual(prefs.active.id, first.id, "the new provider becomes active");
    prefs.modelTarget.defaultModel = "qwen3";
    prefs.changed(0);
    await prefs.flush();
    assert.equal(prefs.error, "");
    assert.equal(prefs.settings.connections.find((c) => c.id === first.id).contextTokens, 32768);
    assert.equal(backend.saves.at(-1).connections.find((c) => c.id === first.id).contextTokens, 32768);
});

test("removing a provider leaves no stale list to save, and the active one stays", async (t) => {
    const backend = connectionBackend();
    const { prefs } = await setup(t, backend.actions);
    await prefs.addConnection(ollamaPreset);
    await prefs.addConnection(ollamaPreset);
    assert.equal(prefs.settings.connections.length, 1, "an unfinished provider must be fixed first");
    assert.match(prefs.connectionError, /Fix the error/);
    prefs.settings.connections[0].defaultModel = "llama3.3";
    prefs.changed(0);
    await prefs.addConnection(ollamaPreset);
    prefs.settings.connections[1].defaultModel = "qwen3";
    prefs.changed(0);
    await prefs.flush();
    assert.equal(prefs.error, "");
    const [first, second] = prefs.settings.connections;
    await prefs.removeConnection(second.id);
    assert.deepEqual(backend.removes, [], "the active provider can't be removed");
    prefs.setProvider("openrouter");
    await prefs.removeConnection(second.id);
    assert.deepEqual(backend.removes, [second.id]);
    assert.deepEqual(prefs.settings.connections.map((c) => c.id), [first.id]);
    prefs.settings.systemPrompt = "After remove";
    prefs.changed(0);
    await prefs.flush();
    assert.equal(prefs.error, "");
    assert.equal(backend.saves.at(-1).systemPrompt, "After remove");
});

test("a provider key is saved after its address, so it binds to the new one", async (t) => {
    const backend = connectionBackend();
    const { prefs, calls } = await setup(t, backend.actions);
    await prefs.addConnection(ollamaPreset);
    const ollama = prefs.settings.connections[0];
    ollama.defaultModel = "llama3.3";
    ollama.baseUrl = "https://ollama.example.com/v1";
    prefs.connectionChanged(10_000);
    assert.equal(prefs.connectionHasKey, false);
    prefs.connectionKeyInput = " fake-test-key ";
    await prefs.saveConnectionKey();
    assert.deepEqual(backend.keyed, [{ id: ollama.id, key: "fake-test-key", baseUrl: "https://ollama.example.com/v1" }]);
    assert.equal(prefs.connectionHasKey, true);
    assert.equal(prefs.connectionKeyInput, "");

    // An address change that can't be saved holds the key back.
    ollama.defaultModel = "";
    prefs.connectionChanged(10_000);
    prefs.connectionKeyInput = "fake-other-key";
    await prefs.saveConnectionKey();
    assert.equal(backend.keyed.length, 1);
    assert.equal(prefs.connectionKeyInput, "fake-other-key");
    assert.ok(prefs.connectionError);
    void calls;
});

test("a late key status for another provider is ignored", async (t) => {
    const backend = connectionBackend();
    let release;
    const { prefs } = await setup(t, { ...backend.actions, hasConnectionKey: (id) => id === "c-000000000001" ? new Promise((resolve) => { release = () => resolve(true); }) : Promise.resolve(false) });
    await prefs.addConnection(ollamaPreset);
    const pending = prefs.refreshConnectionKey();
    prefs.setProvider("openrouter");
    await prefs.refreshConnectionKey();
    release?.();
    await pending;
    assert.equal(prefs.connectionHasKey, false);
});

test("local tools fail closed for missing or explicitly empty roots and preserve the shell flag", async (t) => {
    const { prefs, calls } = await setup(t);
    assert.equal(prefs.settings.localTools, false);
    assert.equal(prefs.settings.allowShellCommands, false);
    assert.equal(prefs.settings.allowHiddenFiles, false);
    assert.deepEqual([...prefs.settings.toolRoots], []);
    prefs.settings.allowShellCommands = true;
    prefs.settings.allowHiddenFiles = true;
    prefs.settings.localTools = true;
    prefs.toolSettingsChanged();
    await prefs.flush();
    assert.deepEqual(calls.saved.at(-1).toolRoots, []);
    assert.equal(calls.saved.at(-1).allowShellCommands, true);
    assert.equal(calls.saved.at(-1).allowHiddenFiles, true);
});

test("tool folders add, edit, remove, and persist an empty list without changing voice or Web", async (t) => {
    const { prefs, calls } = await setup(t);
    assert.equal(prefs.addToolRoot("~/Downloads"), true);
    assert.equal(prefs.addToolRoot("/my code"), true);
    assert.equal(prefs.addToolRoot("/my code"), false);
    assert.match(prefs.toolError, /already/);
    prefs.settings.toolRoots[0] = "~/Documents";
    prefs.toolSettingsChanged();
    await prefs.flush();
    assert.deepEqual(calls.saved.at(-1).toolRoots, ["~/Documents", "/my code"]);
    prefs.removeToolRoot(1);
    prefs.removeToolRoot(0);
    await prefs.flush();
    assert.deepEqual(calls.saved.at(-1).toolRoots, []);
    assert.equal(calls.saved.at(-1).voice.engine, "whisper-server");
    assert.deepEqual(calls.saved.at(-1).webSearch, settings().webSearch);
});

test("tool folder validation rejects relative, empty, oversized, and excessive paths", async (t) => {
    const { prefs, calls } = await setup(t);
    for (const path of ["relative", "", "/bad\0path", "/"+"x".repeat(4096)]) assert.equal(prefs.addToolRoot(path), false);
    assert.equal(calls.saved.length, 0);
    for (let i=0; i<32; i++) assert.equal(prefs.addToolRoot(`/folder-${i}`), true);
    assert.equal(prefs.addToolRoot("/too-many"), false);
    prefs.settings.toolRoots[0] = "relative-edit";
    prefs.toolSettingsChanged();
    await prefs.flush();
    assert.match(prefs.error, /absolute/);
    assert.equal(calls.saved.length, 0);
});

test("tool folder picker adds and replaces roots and cancellation does not save", async (t) => {
    let selected = "/selected";
    const initials = [];
    const { prefs, calls } = await setup(t, { pickToolFolder: async (initial) => { initials.push(initial); return selected; } });
    await prefs.pickToolFolder();
    await prefs.flush();
    assert.deepEqual(calls.saved.at(-1).toolRoots, ["/selected"]);
    selected = "/replacement";
    await prefs.pickToolFolder(0);
    await prefs.flush();
    assert.deepEqual(calls.saved.at(-1).toolRoots, ["/replacement"]);
    const saved = calls.saved.length;
    selected = "";
    await prefs.pickToolFolder(0);
    await prefs.flush();
    assert.equal(calls.saved.length, saved);
    assert.deepEqual(initials, ["", "/selected", "/replacement"]);
    assert.equal(prefs.toolPicking, false);
});

test("removing or editing tool folders invalidates a late picker result", async (t) => {
    const selected = deferred();
    const { prefs } = await setup(t, { pickToolFolder: () => selected.promise });
    prefs.addToolRoot("/first");
    const pending = prefs.pickToolFolder(0);
    prefs.removeToolRoot(0);
    selected.resolve("/must-not-return");
    await pending;
    assert.deepEqual([...prefs.settings.toolRoots], []);
    assert.equal(prefs.toolPicking, false);
});

test("disabling local tools invalidates pending folder additions", async (t) => {
    const selected = deferred();
    const { prefs } = await setup(t, { pickToolFolder: () => selected.promise });
    prefs.settings.localTools = true;
    const pending = prefs.pickToolFolder();
    prefs.settings.localTools = false;
    prefs.toolSettingsChanged();
    selected.resolve("/late");
    await pending;
    assert.deepEqual([...prefs.settings.toolRoots], []);
});

test("reload and disposal ignore old tool pickers without clearing a new picker", async (t) => {
    const first = deferred(), second = deferred();
    let count = 0;
    const { prefs } = await setup(t, { pickToolFolder: () => (++count === 1 ? first.promise : second.promise) });
    const old = prefs.pickToolFolder();
    await prefs.load();
    const current = prefs.pickToolFolder();
    first.resolve("/old");await old;
    assert.equal(prefs.toolPicking, true);
    prefs.dispose();
    second.resolve("/disposed");await current;
    assert.deepEqual([...prefs.settings.toolRoots], []);
});

test("folder picker failures preserve permissions and allow retry", async (t) => {
    const { prefs, calls } = await setup(t, { pickToolFolder: async () => { throw Error("dialog unavailable"); } });
    await prefs.pickToolFolder();
    assert.equal(prefs.toolError, "dialog unavailable");
    assert.equal(prefs.toolPicking, false);
    assert.equal(calls.saved.length, 0);
});

test("legacy Voice settings get separate model profiles and survive model switching", async (t) => {
    const { prefs, calls } = await setup(t);
    assert.deepEqual({ ...prefs.settings.voice.qwen }, { modelDir: "", threads: 4 });
    prefs.settings.voice.engine = "qwen3-asr";
    prefs.settings.voice.qwen.modelDir = "/models/qwen";
    prefs.settings.voice.qwen.threads = 6;
    prefs.voiceChanged();
    prefs.settings.voice.engine = "parakeet";
    prefs.settings.voice.parakeet.modelDir = "/models/parakeet";
    prefs.voiceChanged();
    await prefs.flush();
    const voice = calls.saved.at(-1).voice;
    assert.equal(voice.engine, "parakeet");
    assert.deepEqual(voice.qwen, { modelDir: "/models/qwen", threads: 6 });
    assert.deepEqual(voice.parakeet, { modelDir: "/models/parakeet", threads: 4 });
    assert.equal(voice.whisperUrl, "http://127.0.0.1:8178");
});

test("Cohere gets an English default and saves its own folder, threads, and language", async (t) => {
    const { prefs, calls } = await setup(t, { pickVoiceFolder: async (engine) => {
        assert.equal(engine, "cohere-transcribe");
        return "/models/cohere";
    } });
    assert.deepEqual({ ...prefs.settings.voice.cohere }, { modelDir: "", threads: 4, language: "en" });
    assert.equal(prefs.settings.voice.engine, "whisper-server");
    prefs.settings.voice.qwen.modelDir = "/models/qwen";
    prefs.settings.voice.parakeet.modelDir = "/models/parakeet";
    prefs.settings.voice.engine = "cohere-transcribe";
    prefs.settings.voice.cohere.language = "fr";
    prefs.settings.voice.cohere.threads = 6;
    await prefs.pickVoiceFolder();
    await prefs.flush();
    const saved = calls.saved.at(-1).voice;
    assert.deepEqual(saved.cohere, { modelDir: "/models/cohere", threads: 6, language: "fr" });
    assert.equal(saved.qwen.modelDir, "/models/qwen");
    assert.equal(saved.parakeet.modelDir, "/models/parakeet");
    assert.equal(saved.language, "en");
});

test("changing Cohere language discards obsolete readiness and rejects unsupported languages", async (t) => {
    const result = deferred();
    const { prefs, calls } = await setup(t, { checkVoice: () => result.promise });
    prefs.settings.voice.engine = "cohere-transcribe";
    const checking = prefs.checkVoice();
    prefs.settings.voice.cohere.language = "fr";
    prefs.voiceChanged();
    result.resolve({ ready: true, message: "English model ready" });
    await checking;
    assert.equal(prefs.voiceReadiness, null);
    prefs.settings.voice.cohere.language = "auto";
    prefs.voiceChanged();
    await prefs.flush();
    assert.equal(calls.saved.length, 0);
    assert.match(prefs.error, /Cohere language/);
});

test("obsolete voice readiness results cannot describe a newly selected model", async (t) => {
    const result = deferred();
    const { prefs } = await setup(t, { checkVoice: () => result.promise });
    const checking = prefs.checkVoice();
    prefs.settings.voice.engine = "parakeet";
    prefs.voiceChanged();
    result.resolve({ ready: true, message: "Old model ready" });
    await checking;
    assert.equal(prefs.voiceReadiness, null);
    assert.equal(prefs.voiceChecking, false);
});

test("folder results are ignored after changing model", async (t) => {
    const selected = deferred();
    const { prefs } = await setup(t, { pickVoiceFolder: () => selected.promise });
    prefs.settings.voice.engine = "qwen3-asr";
    prefs.voiceChanged();
    const picking = prefs.pickVoiceFolder();
    prefs.settings.voice.engine = "parakeet";
    prefs.voiceChanged();
    selected.resolve("/obsolete/qwen");
    await picking;
    assert.equal(prefs.settings.voice.qwen.modelDir, "");
    assert.equal(prefs.settings.voice.parakeet.modelDir, "");
    assert.equal(prefs.voicePicking, false);
});

test("canceling the folder picker preserves the model path and does not save", async (t) => {
    const { prefs, calls } = await setup(t, { pickVoiceFolder: async () => "" });
    prefs.settings.voice.engine = "parakeet";
    prefs.settings.voice.parakeet.modelDir = "/keep/this/model";
    await prefs.pickVoiceFolder();
    assert.equal(prefs.settings.voice.parakeet.modelDir, "/keep/this/model");
    assert.equal(calls.saved.length, 0);
    assert.equal(prefs.voicePicking, false);
});

test("chosen model folders save without erasing the other model profile", async (t) => {
    const { prefs, calls } = await setup(t, { pickVoiceFolder: async () => "/my model folder" });
    prefs.settings.voice.engine = "parakeet";
    prefs.settings.voice.qwen.modelDir = "/saved/qwen";
    await prefs.pickVoiceFolder();
    await prefs.flush();
    assert.equal(calls.saved.at(-1).voice.parakeet.modelDir, "/my model folder");
    assert.equal(calls.saved.at(-1).voice.qwen.modelDir, "/saved/qwen");
});

test("missing model readiness does not prevent saving unrelated preferences", async (t) => {
    const { prefs, calls } = await setup(t, { checkVoice: async () => ({ ready: false, message: "Model missing" }) });
    await prefs.checkVoice();
    prefs.settings.systemPrompt = "Updated";
    prefs.changed();
    await prefs.flush();
    assert.equal(prefs.voiceReadiness.ready, false);
    assert.equal(calls.saved.at(-1).systemPrompt, "Updated");
    assert.equal(prefs.error, "");
});

test("invalid CPU thread counts are rejected before saving", async (t) => {
    const { prefs, calls } = await setup(t);
    prefs.settings.voice.qwen.threads = undefined;
    prefs.voiceChanged();
    await prefs.flush();
    assert.equal(calls.saved.length, 0);
    assert.match(prefs.error, /CPU threads/);
});

test("editing unrelated preferences preserves settings-only Web limits", async (t) => {
    const { prefs, calls } = await setup(t);
    prefs.settings.systemPrompt = "Updated prompt";
    prefs.changed();
    await prefs.flush();
    assert.deepEqual(calls.saved[0].webSearch, { maxResults: 4, maxFetches: 2, maxToolCalls: 5 });
});

test("autosave serializes rapid edits without overwriting the draft", async (t) => {
    const first = deferred();
    const writes = [];
    const { prefs } = await setup(t, { save: async (s) => {
        writes.push(s);
        if (writes.length === 1) await first.promise;
    } });
    prefs.settings.systemPrompt = "First edit";
    prefs.changed();
    const saving = prefs.flush();
    prefs.settings.systemPrompt = "Latest edit";
    prefs.settings.voice.language = "auto";
    prefs.changed();
    first.resolve();
    await saving;
    assert.deepEqual(writes.map((s) => s.systemPrompt), ["First edit", "Latest edit"]);
    assert.equal(writes[1].voice.language, "auto");
    assert.equal(prefs.settings.systemPrompt, "Latest edit");
    assert.equal(prefs.status, "Saved");
});

test("save failure retains edits and retry persists the complete settings", async (t) => {
    let fail = true;
    const { prefs, calls } = await setup(t, { save: async (s) => {
        if (fail) throw new Error("Disk unavailable");
        calls.saved.push(s);
    } });
    prefs.settings.keepChatsDays = 90;
    prefs.settings.maxContextChars = 5000;
    prefs.settings.offerScreenshot = false;
    prefs.settings.usePortalShortcuts = true;
    prefs.changed();
    await prefs.flush();
    assert.equal(prefs.error, "Disk unavailable");
    assert.equal(prefs.status, "Unsaved changes");
    fail = false;
    await prefs.flush();
    assert.equal(calls.saved[0].keepChatsDays, 90);
    assert.equal(calls.saved[0].maxContextChars, 5000);
    assert.equal(calls.saved[0].usePortalShortcuts, true);
    assert.equal(calls.saved[0].offerScreenshot, false);
    assert.equal(prefs.error, "");
});

test("fallback controls enforce three unique entries and save an empty list", async (t) => {
    const { prefs, calls } = await setup(t);
    assert.equal(prefs.addFallback("other/model"), false);
    assert.equal(prefs.addFallback("two/model"), true);
    assert.equal(prefs.addFallback("three/model"), true);
    assert.equal(prefs.addFallback("four/model"), false);
    prefs.moveFallback(2, -1);
    assert.deepEqual([...prefs.settings.fallbacks], ["other/model", "three/model", "two/model"]);
    while (prefs.settings.fallbacks.length) prefs.removeFallback(0);
    await prefs.flush();
    assert.deepEqual(calls.saved[0].fallbacks, []);
});

test("invalid numeric input never sends a settings update", async (t) => {
    const { prefs, calls } = await setup(t);
    prefs.settings.maxContextChars = undefined;
    prefs.changed();
    await prefs.flush();
    assert.equal(calls.saved.length, 0);
    assert.match(prefs.error, /whole text context limit/);
    prefs.settings.maxContextChars = 0;
    prefs.changed();
    await prefs.flush();
    assert.equal(calls.saved[0].maxContextChars, 0);
});

test("model-list errors are visible while settings remain usable, and retry recovers", async (t) => {
    let fail = true;
    const { prefs } = await setup(t, { models: async () => {
        if (fail) throw new Error("Network unavailable");
        return [{ id: "available/model", name: "Available", contextLength: 8192 }];
    } });
    await prefs.loadModels();
    assert.equal(prefs.modelsError, "Network unavailable");
    assert.equal(prefs.settings.defaultModel, "test/model");
    fail = false;
    await prefs.loadModels();
    assert.equal(prefs.models[0].id, "available/model");
    assert.equal(prefs.modelsError, "");
});

test("API keys use the keyring action and never enter settings snapshots", async (t) => {
    const { prefs, calls } = await setup(t);
    prefs.keyInput = " test-secret ";
    await prefs.saveKey();
    assert.deepEqual(calls.keys, ["test-secret"]);
    assert.equal(prefs.keyInput, "");
    prefs.changed();
    await prefs.flush();
    assert.equal(JSON.stringify(calls.saved).includes("test-secret"), false);
});

test("keyring failures do not expose the entered secret in error feedback", async (t) => {
    const { prefs } = await setup(t, { setKey: async (key) => { throw new Error(`Rejected ${key}`); } });
    prefs.keyInput = "test-secret";
    await prefs.saveKey();
    assert.equal(prefs.keyInput, "");
    assert.match(prefs.keyError, /keyring/);
    assert.equal(prefs.keyError.includes("test-secret"), false);
});

test("clear-history requires confirmation and refreshes application history", async (t) => {
    const { prefs, calls } = await setup(t);
    await prefs.clearAll();
    assert.equal(prefs.confirmClear, true);
    assert.equal(calls.clear, 0);
    await prefs.clearAll();
    assert.equal(calls.clear, 1);
    assert.equal(calls.cleared, 1);
    assert.equal(prefs.confirmClear, false);
    assert.equal(prefs.status, "History cleared");
});

test("clear-history refuses while the active answer is streaming", async (t) => {
    const { prefs, calls } = await setup(t, { canClear: () => false });
    await prefs.clearAll();
    assert.equal(calls.clear, 0);
    assert.match(prefs.error, /Stop the answer/);
});

test("leaving preferences flushes the last edit before its debounce fires", async (t) => {
    const { prefs, calls } = await setup(t);
    prefs.settings.attachClipboard = false;
    prefs.changed();
    prefs.dispose();
    await settle();
    assert.equal(calls.saved.length, 1);
    assert.equal(calls.saved[0].attachClipboard, false);
});
