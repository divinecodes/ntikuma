import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import { compile } from "svelte/compiler";
import { render } from "svelte/server";
import { JSDOM } from "jsdom";

const component = new URL("../src/components/ToolActivity.svelte", import.meta.url).href;
registerHooks({
    load(url, context, next) {
        if (url === component) return { format: "module", shortCircuit: true,
            source: compile(readFileSync(new URL(url), "utf8"), { filename: new URL(url).pathname, generate: "server" }).js.code };
        return next(url, context);
    },
});
const { default: ToolActivity } = await import(component);

test("tool activity renders safe summaries and distinguishes running, failed and stopped calls", (t) => {
    const activity = [
        { callId: "1", name: "read_file", summary: 'Reading <img src=x onerror="bad()">', status: "started" },
        { callId: "2", name: "list_directory", summary: "Listed ~/Downloads", status: "finished" },
        { callId: "3", name: "read_file", summary: "Read inaccessible.md", status: "error" },
        { callId: "4", name: "read_file", summary: "Read other.md", status: "stopped" },
        { callId: "5", name: "run_command", summary: "Waiting for command approval", status: "waiting" },
        { callId: "6", name: "run_command", summary: "Command df -h", status: "denied" },
    ];
    const dom = new JSDOM(render(ToolActivity, { props: { activity } }).body);
    t.after(() => dom.window.close());
    const document = dom.window.document;
    assert.equal(document.querySelectorAll('ul[aria-label="Tool activity"] li').length, 6);
    assert.equal(document.querySelector(".summary").textContent, activity[0].summary);
    assert.equal(document.querySelector("img, a, [onerror]"), null);
    assert.deepEqual(Array.from(document.querySelectorAll(".state"), (node) => node.textContent), ["Running", "Failed", "Stopped", "Awaiting approval", "Denied"]);
});

test("legacy messages without tool activity render no activity list", () => {
    for (const props of [{}, { activity: [] }]) assert.equal(render(ToolActivity, { props }).body.includes("<ul"), false);
});
