<script lang="ts">
    import { onMount } from "svelte";
    import { app } from "./lib/state.svelte";
    import { popupApi } from "./lib/api";
    import { observePopupSize } from "./lib/popup-size";
    import BarView from "./views/BarView.svelte";
    import ChatView from "./views/ChatView.svelte";
    import HistoryView from "./views/HistoryView.svelte";
    import SettingsView from "./views/SettingsView.svelte";
    import VoiceView from "./views/VoiceView.svelte";

    let card: HTMLDivElement | undefined = $state();

    onMount(() => {
        void app.init();

        if (card) return observePopupSize(card, (height) => { void popupApi.resize(height); });
    });

    function onKeydown(e: KeyboardEvent){
        const ctrl = e.ctrlKey || e.metaKey;
        if (e.key === "Escape"){
            e.preventDefault();
            app.escape();
        } else if (ctrl && e.key.toLowerCase() === "n"){
            e.preventDefault();
            app.newChat();
        } else if (ctrl && e.key.toLowerCase() === "h"){
            e.preventDefault();
            app.goTo("history");
        } else if (ctrl && !e.isComposing && e.key.toLowerCase() === "g" && (app.view === "bar" || app.view === "chat")) {
            e.preventDefault();
            app.toggleWeb();
        } else if(ctrl && e.key === ","){
            e.preventDefault();
            app.goTo("settings");
        } else if (ctrl && e.key === "."){
            e.preventDefault();
            app.stop();
        }
    }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="card" bind:this={card} role="dialog" aria-label="Ntikuma">
    {#if app.view === "bar"}
        <BarView />
    {:else if app.view === "chat"}
        <ChatView />
    {:else if app.view === "voice"}
        {#key app.voiceSession}
            <VoiceView />
        {/key}
    {:else if app.view === "history"}
        <HistoryView />
    {:else if app.view === "settings"}
        <SettingsView />
    {/if}
</div>
<style>
    .card {
        width: 100%;
        background: var(--win);
        border: 1px solid var(--border);
        border-radius: var(--radius);
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
</style>
