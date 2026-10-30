import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import { compile } from "svelte/compiler";
import { render } from "svelte/server";
import { JSDOM } from "jsdom";

const component = new URL("../src/components/CommandApproval.svelte", import.meta.url).href;
registerHooks({ load(url, context, next) {
    if (url === component) return { format: "module", shortCircuit: true,
        source: compile(readFileSync(new URL(url), "utf8"), { filename: new URL(url).pathname, generate: "server" }).js.code };
    return next(url, context);
} });
const { default: CommandApproval } = await import(component);

test("approval cards display the entire exact command as escaped selectable text", (t) => {
    const command = '  printf "<img src=x onerror=bad()>"\n\t'+"printf 'long command'\n".repeat(200);
    const approval = { command, workingDirectory: "/home/test", expiresAt: new Date().toISOString() };
    const dom = new JSDOM(render(CommandApproval, { props: { approval, decide: () => {} } }).body);
    t.after(() => dom.window.close());
    const doc = dom.window.document;
    assert.equal(doc.querySelector("textarea").value, command);
    assert.equal(doc.querySelector("img, [onerror]"), null);
    assert.equal(doc.querySelector('[aria-label="Exact command"]').readOnly, true);
    assert.match(doc.body.textContent, /outside your tool folders/);
    assert.equal(doc.querySelector('[aria-label="Allow command"]').disabled, false);
    assert.equal(doc.querySelector('[aria-label="Deny command"]').disabled, false);
});

test("approval cards disable both buttons while deciding and report RPC errors safely", (t) => {
    const approval = { command: "df -h", workingDirectory: "/home/test", expiresAt: new Date().toISOString() };
    const dom = new JSDOM(render(CommandApproval, { props: { approval, busy: true, error: "<script>bad()</script>", decide: () => {} } }).body);
    t.after(() => dom.window.close());
    const doc = dom.window.document;
    assert.equal(doc.querySelectorAll("button:disabled").length, 2);
    assert.equal(doc.querySelector('[role="alert"]').textContent, "<script>bad()</script>");
    assert.equal(doc.querySelector("script"), null);
});

test("approval cards name where the command's output goes", (t) => {
    for (const [provider, expected] of [["Ollama", "through Ollama"], ["", "through OpenRouter"], [undefined, "through OpenRouter"]]) {
        const approval = { command: "df -h", workingDirectory: "/home/test", expiresAt: new Date().toISOString(), provider };
        const dom = new JSDOM(render(CommandApproval, { props: { approval, decide: () => {} } }).body);
        t.after(() => dom.window.close());
        assert.match(dom.window.document.body.textContent, new RegExp(`Output is sent to your model ${expected}\\.`));
    }
});
