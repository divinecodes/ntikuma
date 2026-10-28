<script lang="ts">
    import { app } from "../lib/state.svelte";
    import { shortWhen } from "../lib/format";
    import Icon from "../components/Icon.svelte";
    import Chips from "../components/Chips.svelte";
    import ModelPicker from "../components/ModelPicker.svelte";
    import WebToggle from "../components/WebToggle.svelte";

    let inputEl: HTMLInputElement | undefined = $state();
    let selected = $state(0);

    $effect(() => {
        void app.focusTick;
        inputEl?.focus();
    });

    $effect(()=> {
        void app.recent.length;
        selected = 0;
    });

    function onKeydown(e: KeyboardEvent){
        if (e.key === "Enter" && !e.shiftKey){
            e.preventDefault();
            if (app.input.trim()) void app.send();
            else if (app.recent[selected]) void app.openChat(app.recent[selected].id);
            return;
        }
        if (!app.input && app.recent.length){
            if (e.key === "ArrowDown"){
                e.preventDefault();
                selected = Math.min(selected + 1, app.recent.length - 1);
            } else if(e.key === "ArrowUp"){
                e.preventDefault();
                selected = Math.max(selected -1, 0);
            }
        }
    }
</script>

<div class="row">
    <span class="logo"><Icon name="logo" size={24} stroke={1.6} /></span>
    <label for="ask" class="sr-only">Ask Ntikuma</label>
    <input
            id="ask"
            bind:this={inputEl}
            bind:value={app.input}
            onkeydown={onKeydown}
            type="text"
            placeholder="Ask Ntikuma anything"
            autocomplete="off"
            spellcheck="false"
    />
    <ModelPicker />
    <WebToggle />
    <button type="button" class="icon-btn round" aria-label="Dictate" onclick={() => app.goTo("voice")}>
        <Icon name="mic" size={20} />
    </button>
</div>

<div class="divider"></div>
<Chips />

{#if app.error}
    <p class="error" role="alert">{app.error}</p>
{/if}

{#if app.recent.length}
    <div class="divider"></div>
    <div class="recent">
        <div class="heading">Recent chats</div>
        {#each app.recent as c, i (c.id)}
            <button
                    type="button"
                    class="item"
                    class:selected={i === selected && !app.input}
                    onclick={() => app.openChat(c.id)}
                    onmouseenter={() => (selected = i)}
            >
                <span class="dim"><Icon name="clock" /></span>
                <span class="title">{c.title}</span>
                <span class="when">{shortWhen(c.updatedAt)}</span>
            </button>
        {/each}
    </div>
{/if}

<div class="hints">
    <span><kbd>Enter</kbd>Send</span>
    <span><kbd>Ctrl+H</kbd>History</span>
    <span><kbd>Ctrl+,</kbd>Settings</span>
    <span><kbd>Esc</kbd>Hide</span>
    <span style="margin-left: auto"><kbd>Super+Shift+Space</kbd>Voice</span>
</div>

<style>
    .row {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 14px 14px 14px 18px;
    }
    .logo {
        color: var(--accent-text);
        display: flex;
    }
    input {
        flex: 1;
        min-width: 0;
        border: none;
        outline: none;
        background: transparent;
        font-size: 19px;
    }
    .divider {
        height: 1px;
        background: var(--border);
    }
    .recent {
        padding: 6px 8px 10px;
    }
    .heading {
        font-size: 12px;
        font-weight: 700;
        color: var(--dim);
        padding: 8px 10px 6px;
    }
    .item {
        width: 100%;
        display: flex;
        align-items: center;
        gap: 12px;
        height: 44px;
        padding: 0 10px;
        border: none;
        border-radius: 8px;
        background: transparent;
        font-size: 14px;
        text-align: left;
    }
    .item.selected {
        background: var(--hover);
    }
    .dim,
    .when {
        color: var(--dim);
        display: flex;
    }
    .when {
        font-size: 12px;
    }
    .title {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
</style>
