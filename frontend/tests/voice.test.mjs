import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import ts from "typescript";
import { compileModule } from "svelte/compiler";

registerHooks({
    load(url, context, next) {
        if (new URL(url).pathname.endsWith("voice.svelte.ts")) {
            const source = ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText;
            return { format: "module", shortCircuit: true, source: compileModule(source, { filename: new URL(url).pathname }).js.code };
        }
        return next(url, context);
    },
});
const { VoiceSession } = await import("../src/lib/voice.svelte.ts");
function deferred() {
    let resolve;
    const promise = new Promise((yes) => { resolve = yes; });
    return { promise, resolve };
}
function setup(t, overrides = {}) {
    const calls = { starts: 0, cancels: 0, sent: [], ids: [], listeners: new Set(), handlers: [] };
    const session = new VoiceSession({
        start: async (id) => { calls.starts++; calls.ids.push(id); },
        onLevel: (handler) => { calls.listeners.add(handler); calls.handlers.push(handler); return () => calls.listeners.delete(handler); },
        stop: async () => "A dictated question",
        cancel: async () => { calls.cancels++; },
        send: async (text) => { calls.sent.push(text); },
        ...overrides,
    });
    t.after(() => session.dispose());
    return { session, calls };
}
const settle = () => new Promise((resolve) => setImmediate(resolve));

test("dictation can stop for review, edit its transcript, and send", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    assert.equal(session.phase, "listening");
    await session.stop("edit");
    assert.equal(session.phase, "review");
    assert.equal(session.transcript, "A dictated question");
    session.transcript = "Edited question";
    await session.send();
    assert.deepEqual(calls.sent, ["Edited question"]);
});

test("stop-and-send transcribes once and submits directly", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    await session.stop("send");
    assert.deepEqual(calls.sent, ["A dictated question"]);
});

test("a replacement session waits for canceled microphone startup", async (t) => {
    const started = deferred();
    const first = setup(t, { start: () => started.promise }).session;
    const pending = first.start();
    await settle();
    const closing = first.dispose();
    const second = setup(t);
    const starting = second.session.start();
    await settle();
    assert.equal(second.calls.starts, 0);
    started.resolve();
    await pending;
    await closing;
    await starting;
    assert.equal(second.calls.starts, 1);
    assert.notEqual(first.phase, "listening");
});

test("dismissing during transcription never submits a late transcript", async (t) => {
    const transcript = deferred();
    const { session, calls } = setup(t, {
        stop: () => transcript.promise,
        cancel: async () => { transcript.resolve("Late transcript"); },
    });
    await session.start();
    const pending = session.stop("send");
    await session.dispose();
    await pending;
    assert.deepEqual(calls.sent, []);
    assert.equal(session.transcript, "");
});

test("microphone and transcription failures can retry a new recording", async (t) => {
    let failStart = true;
    const { session } = setup(t, {
        start: async () => { if (failStart) throw new Error("Microphone unavailable"); },
        stop: async () => { throw new Error("Whisper unavailable"); },
    });
    await session.start();
    assert.equal(session.phase, "error");
    assert.match(session.problem, /Microphone/);
    failStart = false;
    await session.start();
    assert.equal(session.phase, "listening");
    await session.stop("edit");
    assert.equal(session.phase, "error");
    assert.match(session.problem, /Whisper/);
    await session.start();
    assert.equal(session.phase, "listening");
});

test("silence is recoverable and never sends an empty question", async (t) => {
    const { session, calls } = setup(t, { stop: async () => "   " });
    await session.start();
    await session.stop("send");
    assert.equal(session.phase, "error");
    assert.deepEqual(calls.sent, []);
});

test("failed send keeps the transcript available for editing and retry", async (t) => {
    const { session } = setup(t, { send: async () => { throw new Error("Stop the current answer first"); } });
    await session.start();
    await session.stop("send");
    assert.equal(session.phase, "review");
    assert.equal(session.transcript, "A dictated question");
    assert.match(session.problem, /current answer/);
});

test("record-again starts a fresh transcript and duplicate Start is ignored", async (t) => {
    const { session, calls } = setup(t);
    const first = session.start();
    await Promise.all([first, session.start()]);
    assert.equal(calls.starts, 1);
    await session.stop("edit");
    await session.start();
    assert.equal(session.transcript, "");
    assert.equal(calls.starts, 2);
});

function level(calls, value, available = true, id = calls.ids.at(-1)) {
    for (const handler of calls.listeners) handler({ recordingId: id, level: value, available });
}

test("microphone levels distinguish silence and speech and retain bounded measured history", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    assert.equal(calls.listeners.size, 1);
    level(calls, 0);
    assert.equal(session.level, 0);
    assert.equal(session.levelAvailable, true);
    level(calls, 0.1);
    assert.ok(session.level > 0.6 && session.level < 0.7);
    for (let i = 0; i < 100; i++) level(calls, 0.5);
    assert.equal(session.levels.length, 56);
    assert.ok(session.levels.every((x) => x > 0 && x <= 1));
    level(calls, 0, false);
    assert.equal(session.level, 0);
    assert.equal(session.levelAvailable, false);
});

test("stopping, disposal, and failed startup remove level listeners", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    level(calls, 0.5);
    const handler = calls.handlers[0];
    const id = calls.ids[0];
    await session.stop("edit");
    assert.equal(calls.listeners.size, 0);
    handler({ recordingId: id, level: 1, available: true });
    assert.equal(session.level, 0);
    await session.start();
    assert.equal(calls.listeners.size, 1);
    await session.dispose();
    assert.equal(calls.listeners.size, 0);
    const failed = setup(t, { start: async () => { throw new Error("Microphone unavailable"); } });
    await failed.session.start();
    assert.equal(failed.calls.listeners.size, 0);
    assert.equal(failed.session.level, 0);
});

test("late levels from an earlier recording cannot affect its replacement", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    const firstId = calls.ids[0], firstHandler = calls.handlers[0];
    await session.stop("edit");
    await session.start();
    assert.notEqual(calls.ids[1], firstId);
    level(calls, 1, true, firstId);
    firstHandler({ recordingId: firstId, level: 1, available: true });
    assert.equal(session.level, 0);
    level(calls, 0.1);
    assert.ok(session.level > 0);
});

test("malformed level values cannot create invalid meter heights", async (t) => {
    const { session, calls } = setup(t);
    await session.start();
    for (const value of [NaN, Infinity, -0.5]) {
        level(calls, value);
        assert.equal(session.level, 0);
        assert.equal(session.levelAvailable, false);
    }
    level(calls, 5);
    assert.equal(session.level, 1);
});
