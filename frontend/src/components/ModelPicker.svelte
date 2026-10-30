<script lang="ts">
    import { app } from "../lib/state.svelte"
    import { modelName } from "../lib/format"
    import { activeDefaultModel, activeModels } from "../lib/providers"
    import Icon from "./Icon.svelte"

    // The active provider's default plus fallbacks
    const options = $derived(activeModels(app.settings));
    const defaultModel = $derived(activeDefaultModel(app.settings));
    const current = $derived(app.modelOverride || defaultModel);
</script>
<label class="picker">
    <span class="sr-only">Model</span>
    <span class="name">{modelName(current)}</span>
    <Icon name="chevron" size={14} stroke={2} />
    <select
            value={current}
            onchange={(e) => {
      const v = (e.currentTarget as HTMLSelectElement).value;
      app.modelOverride = v === defaultModel ? "" : v;
    }}
    >
        {#each options as m (m)}
            <option value={m}>{modelName(m)}{m === defaultModel ? " (default)" : ""}</option>
        {/each}
    </select>
</label>

<style>
    .picker {
        position: relative;
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 32px;
        padding: 0 10px;
        border-radius: 8px;
        background: var(--hover);
        font-size: 13px;
        flex: none;
    }
    .picker:focus-within {
        outline: 2px solid var(--accent);
        outline-offset: 1px;
    }
    .name {
        max-width: 180px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    select {
        position: absolute;
        inset: 0;
        opacity: 0;
        cursor: pointer;
    }
</style>
