<script lang="ts">
    import { app } from "../lib/state.svelte";
    import Icon from "./Icon.svelte";
</script>

<button type="button" class="web" class:active={app.web} aria-label="Web search" aria-pressed={app.web}
    title="Allow web search (Ctrl+G)" onclick={() => app.toggleWeb()}>
    <Icon name="globe" size={16} />Web
</button>

<style>
    .web {
        flex: none;
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 32px;
        padding: 0 10px;
        border: 1px solid transparent;
        border-radius: 8px;
        background: var(--hover);
        color: var(--dim);
        font-size: 13px;
    }
    .web:hover { border-color: var(--border-strong); }
    .web.active { background: var(--sel); color: var(--sel-text); border-color: var(--accent-text); }
</style>
