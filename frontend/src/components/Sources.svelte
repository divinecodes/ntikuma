<script lang="ts">
    import type { Citation } from "../lib/types";
    import { externalLinks } from "../lib/format";

    let { citations = [] }: { citations?: Citation[] } = $props();
    const sources = $derived.by(() => {
        const seen = new Set<string>();
        return citations.flatMap((citation) => {
            try {
                const url = new URL(citation.url);
                if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || seen.has(citation.url)) return [];
                seen.add(citation.url);
                return [{ ...citation, title: citation.title?.trim() || url.hostname, domain: url.hostname }];
            } catch { return []; }
        });
    });
</script>

{#if sources.length}
    <section class="sources" aria-label="Sources" use:externalLinks>
        <h2>Sources</h2>
        <ol>
            {#each sources as source (source.url)}
                <li><a href={source.url}><span class="title">{source.title}</span><span class="domain">{source.domain}</span></a></li>
            {/each}
        </ol>
    </section>
{/if}

<style>
    .sources { margin: 8px 0; font-size: 12px; user-select: text; }
    h2 { margin: 0 0 6px; font-size: 12px; font-weight: 700; color: var(--dim); }
    ol { margin: 0; padding-left: 22px; }
    li { margin: 0 0 6px; color: var(--dim); }
    a { display: inline-flex; flex-direction: column; max-width: 100%; color: var(--accent-text); text-decoration: none; overflow-wrap: anywhere; }
    a:hover .title { text-decoration: underline; }
    .domain { color: var(--dim); font-size: 11px; }
</style>
