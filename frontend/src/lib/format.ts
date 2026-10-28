import { Marked } from "marked";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { Browser, Clipboard } from "@wailsio/runtime";

function escapeHTML(text: string): string {
    return text.replace(/[&<>"']/g, (char) => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    })[char]!);
}

const marked = new Marked({
    gfm: true,
    breaks: false,
    renderer: {
        // Model HTML is text, never application controls or resource markup.
        html({ text }) { return escapeHTML(text); },
        image({ tokens }) { return this.parser.parseInline(tokens); },
        checkbox({ checked }) { return checked ? "☑ " : "☐ "; },
        code({ text, lang }) {
            const language = lang?.trim().split(/\s+/)[0]?.toLowerCase() || "";
            const supported = Boolean(language && hljs.getLanguage(language));
            const code = supported
                ? hljs.highlight(text, { language, ignoreIllegals: true }).value
                : escapeHTML(text);
            const label = escapeHTML(language || "Plain text");
            return `<div class="code-block"><div class="code-toolbar"><span class="code-language">${label}</span>` +
                '<button type="button" class="code-copy" aria-label="Copy code">Copy code</button>' +
                '<span class="sr-only code-copy-status" role="status" aria-live="polite"></span></div>' +
                `<pre><code class="hljs">${code}</code></pre></div>\n`;
        },
    },
});

/** Renders model Markdown to sanitised HTML. */
export function renderMarkdown(src: string): string {
    const html = marked.parse(src, { async: false }) as string;
    return DOMPurify.sanitize(html, {
        ALLOWED_TAGS: [
            "a", "blockquote", "br", "code", "del", "em", "h1", "h2", "h3", "h4", "h5", "h6",
            "hr", "li", "ol", "p", "pre", "strong", "table", "tbody", "td", "th", "thead", "tr", "ul",
            // Only our code renderer creates these controls and wrappers.
            "button", "div", "span",
        ],
        ALLOWED_ATTR: ["href", "title", "class", "type", "start", "align", "role", "aria-label", "aria-live"],
        ALLOW_DATA_ATTR: false,
        ALLOWED_URI_REGEXP: /^https?:\/\//i,
    });
}

/** Delegated clicks keep code copying available as streaming HTML is replaced. */
export function copyCode(node: HTMLElement) {
    const timers = new Map<HTMLButtonElement, ReturnType<typeof setTimeout>>();
    let destroyed = false;

    const onClick = async (e: MouseEvent) => {
        const button = (e.target as Element | null)?.closest?.<HTMLButtonElement>("button.code-copy");
        if (!button || !node.contains(button) || button.disabled) return;
        const block = button.closest(".code-block");
        const code = block?.querySelector("pre > code");
        const status = block?.querySelector(".code-copy-status");
        if (!code) return;
        e.preventDefault();
        clearTimeout(timers.get(button));
        timers.delete(button);
        button.disabled = true;
        button.textContent = "Copying…";
        if (status) status.textContent = "";
        try {
            await Clipboard.SetText(code.textContent ?? "");
            if (destroyed || !node.contains(button)) return;
            button.textContent = "Copied";
            if (status) status.textContent = "Code copied to clipboard.";
        } catch {
            if (destroyed || !node.contains(button)) return;
            button.textContent = "Copy failed";
            if (status) status.textContent = "Could not copy code. Try again.";
        }
        button.disabled = false;
        timers.set(button, setTimeout(() => {
            if (node.contains(button)) {
                button.textContent = "Copy code";
                if (status) status.textContent = "";
            }
            timers.delete(button);
        }, 2000));
    };

    node.addEventListener("click", onClick);
    return {
        destroy() {
            destroyed = true;
            node.removeEventListener("click", onClick);
            for (const timer of timers.values()) clearTimeout(timer);
            timers.clear();
        },
    };
}

/**
 * Svelte action: links inside rendered answers open in the default browser
 * instead of navigating the popup's webview.
 */
export function externalLinks(node: HTMLElement) {
    const onClick = (e: MouseEvent) => {
        const a = (e.target as HTMLElement | null)?.closest("a");
        if (!a || !node.contains(a)) return;
        e.preventDefault();
        const href = a.getAttribute("href");
        if (href && /^https?:\/\//i.test(href)) void Browser.OpenURL(href);
    };
    node.addEventListener("click", onClick);
    return { destroy: () => node.removeEventListener("click", onClick) };
}

/** "~anthropic/claude-sonnet-latest" -> "Claude Sonnet", "openai/gpt-5.1" -> "GPT-5.1". */
export function modelName(slug: string): string {
    if (!slug) return "";
    const name = (slug.replace(/^~/, "").split("/").pop() ?? slug).replace(/-latest$/, "").replace(/:free$/, "");
    if (/^gpt-/i.test(name)) return "GPT-" + name.slice(4);
    return name
        .split("-")
        .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
        .join(" ");
}

/** Group heading for history lists: "Today", "Yesterday", a weekday, or "Earlier". */
export function dayLabel(iso: string, now = new Date()): string {
    const d = new Date(iso);
    const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const days = Math.round((startOf(now) - startOf(d)) / 86_400_000);
    if (days <= 0) return "Today";
    if (days === 1) return "Yesterday";
    if (days < 7) return d.toLocaleDateString(undefined, { weekday: "long" });
    return "Earlier";
}

/** Short time for list rows: "10:42" today, "Mon", or "2 Oct". */
export function shortWhen(iso: string, now = new Date()): string {
    const d = new Date(iso);
    const label = dayLabel(iso, now);
    if (label === "Today") return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
    if (label === "Yesterday") return "Yesterday";
    const days = (now.getTime() - d.getTime()) / 86_400_000;
    if (days < 7) return d.toLocaleDateString(undefined, { weekday: "short" });
    return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

/** Splits a search snippet on the \x02...\x03 markers the store adds. */
export function snippetParts(snippet: string): { text: string; hit: boolean }[] {
    const parts: { text: string; hit: boolean }[] = [];
    let rest = snippet;
    while (rest.length) {
        const s = rest.indexOf("\x02");
        if (s < 0) {
            parts.push({ text: rest, hit: false });
            break;
        }
        if (s > 0) parts.push({ text: rest.slice(0, s), hit: false });
        const e = rest.indexOf("\x03", s);
        const end = e < 0 ? rest.length : e;
        parts.push({ text: rest.slice(s + 1, end), hit: true });
        rest = e < 0 ? "" : rest.slice(e + 1);
    }
    return parts;
}

/** Highlights each query word inside a plain title. */
export function highlight(text: string, query: string): { text: string; hit: boolean }[] {
    const words = query
        .split(/\s+/)
        .filter(Boolean)
        .map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
    if (!words.length) return [{ text, hit: false }];
    const split = new RegExp(`(${words.join("|")})`, "i");
    const whole = new RegExp(`^(${words.join("|")})$`, "i");
    return text
        .split(split)
        .filter((t) => t !== "")
        .map((t) => ({ text: t, hit: whole.test(t) }));
}
