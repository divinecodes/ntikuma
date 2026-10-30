import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import ts from "typescript";

// Load the real renderer and sanitizer with an isolated DOM and runtime boundary.
const format = new URL("../src/lib/format.ts?markdown-tests", import.meta.url).href;
const purify = "data:text/javascript," + encodeURIComponent(`
    import createDOMPurify from ${JSON.stringify(import.meta.resolve("dompurify"))};
    import { JSDOM } from ${JSON.stringify(import.meta.resolve("jsdom"))};
    export default createDOMPurify(new JSDOM("").window);
`);
const runtime = "data:text/javascript," + encodeURIComponent(`
    export const Clipboard = { SetText: (text) => globalThis.markdownHarness.write(text) };
    export const Browser = { OpenURL: (url) => globalThis.markdownHarness.urls.push(url) };
`);
registerHooks({
    resolve(specifier, context, next) {
        if (context.parentURL === format && specifier === "dompurify") {
            return { url: purify, shortCircuit: true };
        }
        if (context.parentURL === format && specifier === "@wailsio/runtime") {
            return { url: runtime, shortCircuit: true };
        }
        return next(specifier, context);
    },
    load(url, context, next) {
        if (url === format) {
            return { format: "module", shortCircuit: true, source: ts.transpileModule(readFileSync(new URL(url), "utf8"), {
                compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
            }).outputText };
        }
        return next(url, context);
    },
});
const { renderMarkdown, copyCode, externalLinks, answerNote } = await import(format);
const settle = () => new Promise((resolve) => setImmediate(resolve));
const fence = (code, language = "") => `\`\`\`${language}\n${code}\n\`\`\``;

function setup(t, markdown) {
    const dom = new JSDOM("<div class='md'></div>");
    const node = dom.window.document.querySelector(".md");
    node.innerHTML = renderMarkdown(markdown);
    const h = globalThis.markdownHarness = { copied: [], urls: [] };
    h.write = async (text) => { h.copied.push(text); };
    const action = copyCode(node);
    t.after(() => { action.destroy(); dom.window.close(); });
    return { node, h, action };
}

test("Markdown highlights common languages and aliases without changing code text", (t) => {
    const samples = [
        ["go", 'package main\nfunc main() { println("hello") }'],
        ["JS", 'const greeting = "hello";'],
        ["ts title=example.ts", 'const count: number = 42;'],
        ["python", 'def greet():\n    return "hello"'],
        ["bash", 'echo "$HOME"'],
        ["json", '{"count": 42, "enabled": true}'],
        ["html", '<p class="greeting">Hello</p>'],
        ["css", 'body { color: red; }'],
        ["sql", 'SELECT name FROM chats WHERE id = 1;'],
    ];
    const { node } = setup(t, samples.map(([language, code]) => fence(code, language)).join("\n\n"));
    const blocks = node.querySelectorAll(".code-block");
    assert.equal(blocks.length, samples.length);
    samples.forEach(([language, code], index) => {
        assert.equal(blocks[index].querySelector("code").textContent, code);
        assert.ok(blocks[index].querySelector('code [class^="hljs-"]'), language);
        assert.equal(blocks[index].querySelector(".code-language").textContent, language.split(" ")[0].toLowerCase());
        assert.equal(blocks[index].querySelector("button").getAttribute("aria-label"), "Copy code");
    });
});

test("unknown, unlabelled, and indented code stays plain and copyable; inline code stays inline", (t) => {
    const code = '<custom value="a & b">';
    const { node } = setup(t, [fence(code, "unknown-language"), fence(code), "    indented text", "An `inline` example."].join("\n\n"));
    const blocks = node.querySelectorAll(".code-block");
    assert.equal(blocks.length, 3);
    assert.deepEqual(Array.from(blocks, (block) => block.querySelector("code").textContent), [code, code, "indented text"]);
    assert.equal(node.querySelectorAll("code span").length, 0);
    assert.equal(blocks[1].querySelector(".code-language").textContent, "Plain text");
    assert.equal(node.querySelector("p code").textContent, "inline");
    assert.equal(node.querySelectorAll("button.code-copy").length, 3);
});

test("highlighting and language labels cannot turn code into executable HTML", (t) => {
    const code = '<script>alert("bad")</script>\n<img src=x onerror="bad()">\n& < > " \' 你好';
    const { node } = setup(t, fence(code, "html") + "\n\n" + fence(code, 'x"><img/src=x/onerror=bad()>'));
    assert.equal(node.querySelectorAll("script, img, svg, [onerror]").length, 0);
    assert.deepEqual(Array.from(node.querySelectorAll("code"), (el) => el.textContent), [code, code]);
    assert.equal(node.querySelectorAll(".code-copy").length, 2);
    assert.equal(node.querySelectorAll(".code-language")[1].textContent, 'x"><img/src=x/onerror=bad()>');
});

test("Markdown still sanitizes raw HTML and unsafe links outside code blocks", (t) => {
    const { node } = setup(t, '<img src=x onerror="bad()">\n\n<script>bad()</script>\n\n[unsafe](javascript:bad())\n\n**Safe text**');
    assert.equal(node.querySelector("script, [onerror], [href^='javascript:']"), null);
    assert.equal(node.querySelector("strong").textContent, "Safe text");
});

test("Markdown images retain their descriptions without creating resource requests", (t) => {
    const { node } = setup(t, [
        '![Remote preview](https://example.invalid/pixel?context=private)',
        '![Local preview](file:///tmp/private.png)',
        '![Inline preview](data:image/png;base64,aGVsbG8=)',
        '![Relative preview](/wails/runtime?context=private)',
        '![Reference preview][image]\n\n[image]: https://example.invalid/reference.png',
    ].join("\n\n"));
    assert.equal(node.querySelector("img, picture, source, [src], [srcset]"), null);
    for (const description of ["Remote preview", "Local preview", "Inline preview", "Relative preview", "Reference preview"]) {
        assert.ok(node.textContent.includes(description));
    }
});

test("raw model HTML cannot create controls, native actions, CSS resources or navigation", (t) => {
    const raw = [
        '<form action="https://example.invalid/collect"><input name="context" value="private"><button type="submit">Continue</button></form>',
        '<div style="background-image:url(https://example.invalid/pixel)">Preview</div>',
        '<style>@import "https://example.invalid/style.css";</style>',
        '<button data-wml-event="chat:done" data-wml-openurl="file:///tmp/private">Native action</button>',
        '<iframe src="https://example.invalid/frame"></iframe>',
        '<video poster="https://example.invalid/poster"><source src="https://example.invalid/video"></video>',
        '<svg><image href="https://example.invalid/svg"></image></svg>',
        '<meta http-equiv="refresh" content="0;url=https://example.invalid/">',
        '<base href="https://example.invalid/"><link rel="stylesheet" href="https://example.invalid/style.css">',
    ];
    for (const html of raw) {
        const { node } = setup(t, html);
        assert.equal(node.querySelector("form, input, button, style, iframe, video, source, svg, meta, base, link, [style], [data-wml-event], [data-wml-openurl]"), null);
        assert.ok(node.textContent.includes(html), "raw HTML should remain readable text");
    }
});

test("restricted Markdown preserves formatting and exposes only HTTP browser links", async (t) => {
    const { node, h } = setup(t, [
        '# Heading\n\n**Strong** and *emphasis*, ~~removed~~ and `inline code`.',
        '> Quoted text\n\n1. First\n2. Second',
        '- [x] Done\n- [ ] Later',
        '| Column | Other |\n| --- | --- |\n| Value | Another |',
        '[HTTPS](https://example.com/https) [HTTP](http://example.com/http)',
        '[File](file:///tmp/private) [Mail](mailto:someone@example.com) [Relative](/wails/runtime) [Data](data:text/html,private)',
        fence('const value = "safe";', "js"),
    ].join("\n\n"));
    for (const tag of ["h1", "strong", "em", "del", "code", "blockquote", "ol", "li", "table", "th", "td"]) {
        assert.ok(node.querySelector(tag), tag);
    }
    assert.equal(node.querySelector("input"), null);
    assert.ok(node.textContent.includes("☑ Done"));
    assert.ok(node.textContent.includes("☐ Later"));
    assert.deepEqual(Array.from(node.querySelectorAll("a[href]"), (a) => a.getAttribute("href")), ["https://example.com/https", "http://example.com/http"]);
    const links = externalLinks(node);
    t.after(() => links.destroy());
    for (const a of node.querySelectorAll("a")) a.click();
    node.querySelector(".code-copy").click();
    await settle();
    assert.deepEqual(h.urls, ["https://example.com/https", "http://example.com/http"]);
    assert.deepEqual(h.copied, ['const value = "safe";']);
});

test("copying one code block preserves whitespace and Unicode without the toolbar or Markdown fences", async (t) => {
    const code = '\tif (a < b && name === "Zoë") {\n    console.log("你好");\n}\n\n// final line';
    const { node, h } = setup(t, fence("first block", "text") + "\n\n" + fence(code, "js"));
    const button = node.querySelectorAll(".code-copy")[1];
    button.click();
    await settle();
    assert.deepEqual(h.copied, [code]);
    assert.equal(button.textContent, "Copied");
    assert.equal(button.disabled, false);
    assert.equal(node.querySelectorAll(".code-copy-status")[1].textContent, "Code copied to clipboard.");
});

test("copy controls work after incomplete streaming code is replaced and more blocks arrive", async (t) => {
    const { node, h } = setup(t, '```js\nconst value = "hel');
    node.querySelector(".code-copy").click();
    await settle();
    node.innerHTML = renderMarkdown(fence('const value = "hello";', "js") + "\n\n" + fence("print(value)", "python"));
    const buttons = node.querySelectorAll(".code-copy");
    buttons[0].click();
    await settle();
    buttons[1].click();
    await settle();
    assert.deepEqual(h.copied, ['const value = "hel', 'const value = "hello";', "print(value)"]);
    assert.ok(node.querySelector("code .hljs-keyword"));
});

test("clipboard failure is reported and the same button can retry", async (t) => {
    const { node, h } = setup(t, fence("hello", "text"));
    h.write = async () => { throw new Error("Clipboard unavailable"); };
    const button = node.querySelector(".code-copy");
    button.click();
    await settle();
    assert.equal(button.textContent, "Copy failed");
    assert.equal(button.disabled, false);
    assert.equal(node.querySelector("[role=status]").textContent, "Could not copy code. Try again.");
    h.write = async (text) => h.copied.push(text);
    button.click();
    await settle();
    assert.deepEqual(h.copied, ["hello"]);
    assert.equal(button.textContent, "Copied");
});

test("pending clipboard requests capture the clicked code and ignore duplicate clicks", async (t) => {
    const { node, h } = setup(t, fence("original", "text"));
    let finish;
    h.write = (text) => {
        h.copied.push(text);
        return new Promise((resolve) => { finish = resolve; });
    };
    const button = node.querySelector(".code-copy");
    button.click();
    button.click();
    node.querySelector("code").textContent = "changed while copying";
    assert.deepEqual(h.copied, ["original"]);
    assert.equal(button.disabled, true);
    finish();
    await settle();
    assert.equal(button.textContent, "Copied");
});

test("late clipboard results cannot change replacement code blocks", async (t) => {
    const { node, h } = setup(t, "");
    for (const fails of [false, true]) {
        node.innerHTML = renderMarkdown(fence("old code"));
        let finish;
        h.write = () => new Promise((resolve, reject) => { finish = fails ? reject : resolve; });
        node.querySelector(".code-copy").click();
        node.innerHTML = renderMarkdown(fence("new code"));
        finish(new Error("old failure"));
        await settle();
        assert.equal(node.querySelector(".code-copy").textContent, "Copy code");
        assert.equal(node.querySelector("[role=status]").textContent, "");
    }
});

test("copy feedback resets and destroying the action removes its listener", async (t) => {
    const { node, h, action } = setup(t, fence("hello"));
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const button = node.querySelector(".code-copy");
    button.click();
    await settle();
    assert.equal(button.textContent, "Copied");
    t.mock.timers.tick(2000);
    assert.equal(button.textContent, "Copy code");
    assert.equal(node.querySelector("[role=status]").textContent, "");
    action.destroy();
    button.click();
    await settle();
    assert.deepEqual(h.copied, ["hello"]);
});

test("destroying the action ignores an outstanding clipboard result", async (t) => {
    const { node, h, action } = setup(t, fence("hello"));
    let finish;
    h.write = () => new Promise((resolve) => { finish = resolve; });
    const button = node.querySelector(".code-copy");
    button.click();
    action.destroy();
    finish();
    await settle();
    assert.equal(button.textContent, "Copying…");
    assert.equal(node.querySelector("[role=status]").textContent, "");
});

test("copy controls and external browser links work together", async (t) => {
    const { node, h } = setup(t, '[Documentation](https://example.com/docs)\n\n' + fence("hello"));
    const links = externalLinks(node);
    t.after(() => links.destroy());
    node.querySelector(".code-copy").click();
    await settle();
    node.querySelector("a").click();
    assert.deepEqual(h.copied, ["hello"]);
    assert.deepEqual(h.urls, ["https://example.com/docs"]);
});

test("answer notes name a fallback model and the direct provider that answered", () => {
    assert.equal(answerNote({ model: "openai/gpt-5.1", fallback: true }), "Answered by GPT-5.1 · fallback");
    assert.equal(answerNote({ model: "anthropic/claude-sonnet-5.5" }), "");
    assert.equal(answerNote({ model: "llama3.3", provider: "Ollama" }), "Answered by Llama3.3 on Ollama");
    assert.equal(answerNote({ provider: "Ollama" }), "Answered by Ollama");
    assert.equal(answerNote({ model: "qwen3", fallback: true, provider: "Ollama" }), "Answered by Qwen3 on Ollama · fallback");
});
