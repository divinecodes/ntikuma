import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { test } from "node:test";
import { compile } from "svelte/compiler";
import { render } from "svelte/server";
import { JSDOM } from "jsdom";

const sources = new URL("../src/components/Sources.svelte", import.meta.url).href;
registerHooks({
    resolve(specifier, context, next) {
        if (context.parentURL === sources && specifier === "../lib/format") {
            // Server rendering omits DOM actions; externalLinks itself is covered separately.
            return { url: "data:text/javascript,export function externalLinks() {}", shortCircuit: true };
        }
        return next(specifier, context);
    },
    load(url, context, next) {
        if (url === sources) return { format: "module", shortCircuit: true, source: compile(readFileSync(new URL(url), "utf8"), { filename: new URL(url).pathname, generate: "server" }).js.code };
        return next(url, context);
    },
});
const { default: Sources } = await import(sources);

test("Sources renders an ordered list with safe titles, domains, and deduplicated links", (t) => {
    const html = render(Sources, { props: { citations: [
        { url: "https://go.dev/doc/", title: '<img src=x onerror="bad()">' },
        { url: "https://example.com/page", title: "" },
        { url: "https://go.dev/doc/", title: "Repeated" },
        { url: "javascript:bad()", title: "Unsafe" },
        { url: "file:///tmp/secret", title: "Local" },
        { url: "not a URL", title: "Malformed" },
    ] } }).body;
    const dom = new JSDOM(html);
    t.after(() => dom.window.close());
    const document = dom.window.document;
    assert.equal(document.querySelectorAll("ol li").length, 2);
    assert.equal(document.querySelector(".title").textContent, '<img src=x onerror="bad()">');
    assert.equal(document.querySelector("img, [onerror]"), null);
    assert.deepEqual(Array.from(document.querySelectorAll("a"), (a) => a.href), ["https://go.dev/doc/", "https://example.com/page"]);
    assert.deepEqual(Array.from(document.querySelectorAll(".domain"), (a) => a.textContent), ["go.dev", "example.com"]);
    assert.equal(document.querySelectorAll(".title")[1].textContent, "example.com");
});

test("Sources renders nothing for legacy or invalid metadata", () => {
    for (const props of [{}, { citations: [] }, { citations: [{ url: "data:text/html,bad", title: "Bad" }] }]) {
        assert.equal(render(Sources, { props }).body.includes("<section"), false);
    }
});
