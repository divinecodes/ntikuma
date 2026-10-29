<script lang="ts">
    import { tick } from "svelte";
    import { Clipboard } from "@wailsio/runtime";
    import { app } from "../lib/state.svelte";
    import { copyCode, externalLinks, modelName, renderMarkdown } from "../lib/format";
    import Icon from "../components/Icon.svelte";
    import Chips from "../components/Chips.svelte";
    import ModelPicker from "../components/ModelPicker.svelte";
    import WebToggle from "../components/WebToggle.svelte";
    import Sources from "../components/Sources.svelte";
    import ToolActivity from "../components/ToolActivity.svelte";
    import CommandApproval from "../components/CommandApproval.svelte";

    let inputEl: HTMLInputElement | undefined = $state();
    let scroller: HTMLDivElement | undefined = $state();
    let stick = true; // follow the stream unless the user scroll up.

    $effect(() => {
        void app.focusTick;
        if (app.approval) return;
        inputEl?.focus({ preventScroll: true });
    });

    //keep the newest text in view while streaming
    $effect(() => {
        const last = app.messages[app.messages.length - 1];
        void last?.content;
        void last?.activity;
        void last?.streaming;
        void app.approval;
        void app.messages.length;
        if(stick) tick().then(() => scroller?.scrollTo({top: scroller.scrollHeight}));
    });

    function onScroll() {
        if(!scroller) return;
        stick  = scroller.scrollHeight - scroller.scrollTop -scroller.clientHeight < 40;
    }

    function onKeydown(e: KeyboardEvent){
        if (e.key === "Enter" && !e.shiftKey){
            e.preventDefault();
            void app.send();
        }
    }

    const  streamingModel = $derived(app.modelOverride || app.settings?.defaultModel || "");
    const lastId = $derived(app.messages[app.messages.length - 1]?.id);
</script>

<header>
    <button type="button" class="icon-btn" aria-label="New chat" onclick={() => app.newChat()}>
        <Icon name="edit" size={18} />
    </button>
    <h1>{app.chatTitle || "New chat"}</h1>
    <button type="button" class="icon-btn" aria-label="History" onclick={() => app.goTo("history")}>
        <Icon name="clock" size={18} />
    </button>
</header>

<div class="messages" bind:this={scroller} onscroll={onScroll}>
    {#each app.messages as m (m.id)}
        {#if m.role === "user"}
            <div class="user">
                {#each m.attachments ?? [] as a (a.kind + a.label)}
          <span class="sent-chip">
            <Icon name={a.kind === "screenshot" ? "screenshot" : "clipboard"} size={13} />{a.label}
          </span>
                {/each}
                <div class="bubble">{m.content}</div>
            </div>
        {:else}
            <div class="assistant">
                {#if m.fallback && m.model}
                    <div class="meta">
                        <span class="fallback"><Icon name="swap" size={13} stroke={2} />Answered by {modelName(m.model)} · fallback</span>
                    </div>
                {/if}
                <div class="md" class:streaming={m.streaming} use:externalLinks use:copyCode>{@html renderMarkdown(m.content)}</div>
                <Sources citations={m.citations} />
                <ToolActivity activity={m.activity} />
                {#if app.approval?.messageId === m.id}
                    {#key app.approval.callId}
                        <CommandApproval approval={app.approval} busy={app.approvalBusy} error={app.approvalError} focusTick={app.focusTick} decide={(allow) => { void app.answerApproval(allow); }} />
                    {/key}
                {/if}
                {#if m.error}
                    <p class="msg-error" role="alert">{m.error}</p>
                {/if}
                {#if m.stopped}
                    <p class="note">Stopped</p>
                {/if}
                {#if (!m.streaming && m.content) || (app.canContinue && m.id === lastId)}
                    <div class="actions">
                        {#if app.canContinue && m.id === lastId}
                            <button type="button" class="copy continue" onclick={() => { void app.continueAnswer(); }}>
                                <Icon name="send" size={14} />Continue
                            </button>
                        {/if}
                        {#if !m.streaming && m.content}
                            <button type="button" class="copy" onclick={() => Clipboard.SetText(m.content)}>
                                <Icon name="copy" size={14} />Copy
                            </button>
                        {/if}
                    </div>
                {/if}
            </div>
        {/if}
    {/each}
</div>

{#if app.error}
    <p class="error" role="alert">{app.error}</p>
{/if}

{#if app.attachments.length || app.settings?.offerScreenshot || app.screenshotPending || app.settings?.attachSelection}
    <div class="composer-chips"><Chips showAutoAttach={false} /></div>
{/if}

<div class="composer">
    <label for="followup" class="sr-only">Ask a follow-up</label>
    <input
            id="followup"
            bind:this={inputEl}
            bind:value={app.input}
            onkeydown={onKeydown}
            type="text"
            placeholder={app.approval ? "Approve or deny the command" : app.streaming ? "Answering…" : "Ask a follow-up"}
            autocomplete="off"
    />
    <ModelPicker />
    <WebToggle />
    <button type="button" class="icon-btn round" aria-label="Dictate" onclick={() => app.goTo("voice")}>
        <Icon name="mic" size={20} />
    </button>
    {#if app.streaming}
        <button type="button" class="btn destructive" onclick={() => app.stop()}>
            <Icon name="stop" size={14} />Stop
        </button>
    {:else}
        <button type="button" class="btn primary" disabled={!app.input.trim()} onclick={() => app.send()}>
            <Icon name="send" size={16} stroke={2.2} />Send
        </button>
    {/if}
</div>

<div class="hints">
    {#if app.streaming}
        <span class="status" role="status"><span class="pulse"></span><span class="status-label">{app.toolHint || (app.searchingWeb ? "Searching the web…" : `Answering with ${modelName(streamingModel)}`)}</span></span>
        <span style="margin-left: auto"><kbd>Ctrl+.</kbd>Stop</span>
        <span><kbd>Esc</kbd>{app.approval ? "Deny command" : "Hide, answer keeps streaming"}</span>
    {:else}
        <span><kbd>Enter</kbd>Send</span>
        <span><kbd>Ctrl+N</kbd>New chat</span>
        <span><kbd>Ctrl+H</kbd>History</span>
        <span style="margin-left: auto"><kbd>Esc</kbd>Hide</span>
    {/if}
</div>

<style>
    header {
        display: flex;
        align-items: center;
        gap: 8px;
        height: 52px;
        padding: 0 10px;
        border-bottom: 1px solid var(--border);
    }
    h1 {
        flex: 1;
        min-width: 0;
        margin: 0;
        text-align: center;
        font-size: 14px;
        font-weight: 700;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .messages {
        max-height: 460px;
        min-height: 120px;
        overflow-y: auto;
        display: flex;
        flex-direction: column;
        gap: 16px;
        padding: 16px 24px;
    }
    .user {
        align-self: flex-end;
        max-width: 78%;
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        gap: 6px;
    }
    .bubble {
        background: var(--hover);
        border-radius: 14px;
        padding: 10px 14px;
        font-size: 15px;
        line-height: 1.45;
        white-space: pre-wrap;
        user-select: text;
    }
    .sent-chip {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 26px;
        padding: 0 10px;
        border-radius: 13px;
        border: 1px solid var(--border);
        color: var(--dim);
        font-size: 12px;
    }
    .assistant {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .meta {
        display: flex;
    }
    .fallback {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 26px;
        padding: 0 10px;
        border-radius: 13px;
        background: var(--warn-tint);
        color: var(--warn-text);
        font-size: 12px;
        font-weight: 700;
    }
    /* Streaming caret sits at the end of the last block, code included. */
    .md.streaming:empty::after,
    .md.streaming :global(> :last-child)::after,
    .md.streaming :global(> pre:last-child code)::after,
    .md.streaming :global(> .code-block:last-child code)::after {
        content: "";
        display: inline-block;
        width: 8px;
        height: 16px;
        margin-left: 2px;
        vertical-align: -3px;
        background: var(--accent-text);
    }
    .md.streaming :global(> pre:last-child)::after,
    .md.streaming :global(> .code-block:last-child)::after {
        content: none;
    }
    .msg-error {
        margin: 0;
        color: var(--red);
        font-size: 13px;
    }
    .note {
        margin: 0;
        color: var(--dim);
        font-size: 12px;
    }
    .copy {
        align-self: flex-start;
        display: inline-flex;
        align-items: center;
        gap: 6px;
        height: 28px;
        padding: 0 8px;
        border: none;
        border-radius: 6px;
        background: transparent;
        color: var(--dim);
        font-size: 12px;
    }
    .copy:hover {
        background: var(--hover);
    }
    .actions {
        display: flex;
        gap: 4px;
    }
    .continue {
        color: var(--accent-text);
    }
    .composer-chips {
        border-top: 1px solid var(--border);
    }
    .composer {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 10px 12px 10px 18px;
        border-top: 1px solid var(--border);
    }
    .composer input {
        flex: 1;
        min-width: 0;
        border: none;
        outline: none;
        background: transparent;
        font-size: 16px;
    }
    .btn:disabled {
        opacity: 0.5;
        cursor: default;
    }
    .status {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
        flex: 1;
    }
    .status-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .pulse {
        flex: none;
        width: 8px;
        height: 8px;
        border-radius: 4px;
        background: var(--accent-text);
    }
    @media (prefers-reduced-motion: no-preference) {
        .md.streaming:empty::after,
        .md.streaming :global(> :last-child)::after,
        .md.streaming :global(> pre:last-child code)::after {
            animation: blink 1s steps(2, start) infinite;
        }
        .pulse {
            animation: pulse 1.4s ease-in-out infinite;
        }
    }
    @keyframes blink {
        to {
            opacity: 0;
        }
    }
    @keyframes pulse {
        50% {
            opacity: 0.35;
        }
    }
</style>
