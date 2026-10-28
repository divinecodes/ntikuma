<script lang="ts">
    import { app } from  "../lib/state.svelte";
    import Icon from "./Icon.svelte"

    let { showAutoAttach  = true}: { showAutoAttach?: boolean} = $props()

    const hasClipboard = $derived(app.attachments.some((a) => a.kind === "clipboard"));
    const hasShot = $derived(app.attachments.some((a) => a.kind === "screenshot"));
    const hasSelection = $derived(app.attachments.some((a) => a.kind === "selection"));
</script>
<div class="chips">
    <span class="label">Context</span>
    {#each app.attachments as a, i (a.kind + a.label)}
    <span class="chip">
      <Icon name={a.kind === "screenshot" ? "screenshot" : "clipboard"} size={14} />
      <span>{a.label}</span>
      <button type="button" aria-label="Remove {a.label}" onclick={() => app.removeAttachment(i)}>
        <Icon name="x" size={12} stroke={2.2} />
      </button>
    </span>
    {/each}
    {#if app.attachments.length === 0}
        <span class="empty">No new context attached.</span>
    {/if}
    {#if !hasClipboard}
        <button type="button" class="add" onclick={() => app.captureClipboard()}>
            <Icon name="plus" size={13} stroke={2} />Clipboard
        </button>
    {/if}
    {#if !hasShot && app.settings?.offerScreenshot}
        <button type="button" class="add" disabled={app.screenshotPending} aria-busy={app.screenshotPending} onclick={() => app.addScreenshot()}>
            <Icon name="plus" size={13} stroke={2} />{app.screenshotPending ? "Choosing screenshot…" : "Screenshot"}
        </button>
    {/if}
    {#if !hasSelection && app.settings?.attachSelection}
        <button type="button" class="add" disabled={app.selectionPending || !app.selectionSupport?.available} aria-busy={app.selectionPending} onclick={() => app.captureSelection(true)}>
            <Icon name="plus" size={13} stroke={2} />{app.selectionPending ? "Reading selection…" : "Selected text"}
        </button>
    {/if}
    {#if showAutoAttach}
        <button
                type="button"
                class="auto"
                role="switch"
                aria-checked={app.autoAttach}
                onclick={() => app.toggleAutoAttach()}
        >
            Auto-attach clipboard
            <span class="track" class:on={app.autoAttach}><span class="knob"></span></span>
        </button>
    {/if}
    {#if app.settings?.attachSelection && (app.selectionMessage || app.selectionSupport?.available === false)}
        <span class="selection-note" role="status">{app.selectionMessage || app.selectionSupport?.message}</span>
    {/if}
</div>

<style>
    .selection-note {
        flex-basis: 100%;
        color: var(--dim);
        font-size: 12px;
    }
    .chips {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
        padding: 10px 18px;
        min-height: 52px;
    }
    .label {
        font-size: 12px;
        font-weight: 700;
        color: var(--dim);
        margin-right: 2px;
    }
    .chip {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 30px;
        padding: 0 3px 0 10px;
        border-radius: 15px;
        background: var(--sel);
        color: var(--sel-text);
        font-size: 13px;
    }
    .chip button {
        width: 24px;
        height: 24px;
        border-radius: 12px;
        border: none;
        background: transparent;
        color: inherit;
        display: grid;
        place-items: center;
    }
    .chip button:hover {
        background: rgba(127, 127, 127, 0.18);
    }
    .empty {
        font-size: 13px;
        color: var(--dim);
    }
    .add {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 30px;
        padding: 0 10px;
        border-radius: 15px;
        border: 1px solid var(--border);
        background: transparent;
        font-size: 13px;
    }
    .add:hover {
        background: var(--hover);
    }
    .auto {
        margin-left: auto;
        display: inline-flex;
        align-items: center;
        gap: 8px;
        border: none;
        background: transparent;
        font-size: 13px;
        color: var(--dim);
        padding: 4px 0;
    }
    .track {
        position: relative;
        width: 40px;
        height: 22px;
        border-radius: 11px;
        background: var(--border-strong);
    }
    .track.on {
        background: var(--accent);
    }
    .knob {
        position: absolute;
        top: 2px;
        left: 2px;
        width: 18px;
        height: 18px;
        border-radius: 9px;
        background: #ffffff;
        transition: transform 120ms;
    }
    .track.on .knob {
        transform: translateX(18px);
    }
</style>
