<script lang="ts">
  import { app } from "../lib/state.svelte";
  import { onMount } from "svelte";
  import { historyApi } from "../lib/api";
  import { HistoryBrowser } from "../lib/history.svelte";
  import { copyCode, dayLabel, externalLinks, highlight, modelName, renderMarkdown, shortWhen, snippetParts } from "../lib/format";
  import Icon from "../components/Icon.svelte";
  import Sources from "../components/Sources.svelte";
  import ToolActivity from "../components/ToolActivity.svelte";

  const history = new HistoryBrowser({
    ...historyApi,
    open: async (id) => {
      app.error = "";
      await app.openChat(id);
      if (app.error) throw new Error(app.error);
    },
    remove: async (id) => {
      if (app.streaming && app.chatId === id) throw new Error("Stop the answer before deleting this chat.");
      await historyApi.remove(id);
    },
    deleted: (id) => app.forgetDeletedChat(id),
  });
  const query = $derived(history.query);
  const hits = $derived(history.hits);
  const selected = $derived(history.selected);
  const detail = $derived(history.detail);
  const selectedChat = $derived(hits[selected]?.chat);
  const cannotDelete = $derived(Boolean(history.deleting || history.opening || !selectedChat ||
    (app.streaming && app.chatId === selectedChat.id)));
  let searchEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLDivElement | undefined = $state();

  onMount(() => {
    history.search("", 0);
    return () => history.dispose();
  });

  $effect(() => {
    void app.focusTick;
    searchEl?.focus();
  });

  $effect(() => {
    void history.selected;
    listEl?.querySelector('[data-selected="true"]')?.scrollIntoView({ block: "nearest" });
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
    const target = e.target;
    if (target instanceof Element && target.closest("button, a, input, textarea, select") &&
      target !== searchEl && !target.closest(".item")) return;
    if (history.keydown(e.key)) e.preventDefault();
  }

  // Group headings: only show one when it changes.
  const groups = $derived(
    hits.map((h, i) => {
      const g = dayLabel(h.chat.updatedAt);
      return i === 0 || dayLabel(hits[i - 1].chat.updatedAt) !== g ? g : "";
    }),
  );
</script>

<svelte:window onkeydown={onKeydown} />

<div class="split">
  <aside aria-label="Chat history">
    <div class="search">
      <label for="search" class="sr-only">Search chats</label>
      <div class="entry">
        <span class="dim"><Icon name="search" stroke={2} /></span>
        <input
          id="search"
          type="search"
          bind:this={searchEl}
          value={query}
          oninput={(e) => history.search(e.currentTarget.value)}
          placeholder="Search chats"
          autocomplete="off"
        />
      </div>
      {#if query.trim() && !history.loading}
        <div class="count" role="status">{hits.length} {hits.length === 1 ? "chat matches" : "chats match"}</div>
      {/if}
    </div>
    <div class="list" bind:this={listEl} aria-busy={history.loading}>
      {#if history.loading}<p class="none" role="status">Loading chats...</p>{/if}
      {#each hits as h, i (h.chat.id)}
        {#if groups[i]}
          <div class="group">{groups[i]}</div>
        {/if}
        <button
          type="button"
          class="item"
          class:selected={i === selected}
          aria-pressed={i === selected}
          data-selected={i === selected}
          disabled={history.opening}
          onclick={() => history.select(i)}
          ondblclick={() => history.openSelected()}
        >
          <span class="line">
            <span class="title">
              {#each highlight(h.chat.title, query) as p, j (j)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}
            </span>
            <span class="when">{shortWhen(h.chat.updatedAt)}</span>
          </span>
          {#if h.snippet}
            <span class="snippet">
              {#each snippetParts(h.snippet) as p, j (j)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}
            </span>
          {/if}
        </button>
      {:else}
        {#if !history.loading && !history.error}<p class="none">{query.trim() ? "No chats match." : "No chats yet."}</p>{/if}
      {/each}
    </div>
  </aside>

  <section aria-label="Conversation preview">
      <header>
        <h1>{selectedChat?.title ?? "History"}</h1>
        <button type="button" class="btn primary small" disabled={!selectedChat || history.opening || history.deleting === selectedChat.id} onclick={() => history.openSelected()}>
          {history.opening ? "Opening..." : "Continue chat"}
        </button>
        <button type="button" class="icon-btn" aria-label="Delete chat" disabled={cannotDelete}
          title={app.streaming && selectedChat?.id === app.chatId ? "Stop the answer before deleting this chat" : "Delete chat"}
          onclick={() => history.removeSelected()}>
          <Icon name="trash" size={17} />
        </button>
        <button type="button" class="icon-btn" aria-label="Back" onclick={() => app.back()}>
          <Icon name="x" stroke={2} />
        </button>
      </header>
    {#if history.error}<p class="error" role="alert">{history.error}</p>{/if}
    {#if history.previewLoading}
      <p class="empty" role="status">Loading conversation...</p>
    {:else if history.previewError}
      <div class="preview">
        <p class="error" role="alert">{history.previewError}</p>
        <button type="button" class="btn" onclick={() => history.select(selected)}>Retry preview</button>
      </div>
    {:else if detail}
      <div class="preview" aria-label="Messages">
        {#each detail.messages as m (m.id)}
          {#if m.role === "user"}
            <div class="bubble">{m.content}</div>
          {:else if m.role === "assistant"}
            {#if m.fallback && m.model}
              <span class="fallback"><Icon name="swap" size={13} stroke={2} />Answered by {modelName(m.model)} · fallback</span>
            {/if}
            <div class="md" use:externalLinks use:copyCode>{@html renderMarkdown(m.content)}</div>
            <Sources citations={m.citations} />
            <ToolActivity activity={m.activity} />
            {#if m.error}<p class="error" role="alert">{m.error}</p>{/if}
            {#if m.stopped}<p class="dim">Stopped</p>{/if}
          {/if}
        {:else}
          <p class="none">This chat has no messages yet.</p>
        {/each}
      </div>
    {:else}
      <div class="empty">
        Pick a chat to preview it.
      </div>
    {/if}
  </section>
</div>

<div class="hints">
  <span><kbd>Up/Down</kbd>Move</span>
  <span><kbd>Enter</kbd>Continue chat</span>
  <span><kbd>Delete</kbd>Delete with search empty</span>
  <span style="margin-left: auto"><kbd>Esc</kbd>Back</span>
</div>

<style>
  .split {
    display: flex;
    height: 480px;
  }
  aside {
    width: 270px;
    flex: none;
    display: flex;
    flex-direction: column;
    background: var(--side);
    border-right: 1px solid var(--border);
  }
  .search {
    padding: 10px 10px 6px;
  }
  .entry {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 10px;
    border-radius: 8px;
    background: var(--view);
    border: 2px solid var(--accent);
  }
  .entry input {
    flex: 1;
    min-width: 0;
    border: none;
    outline: none;
    background: transparent;
  }
  .dim {
    color: var(--dim);
    display: flex;
  }
  .count {
    font-size: 12px;
    color: var(--dim);
    padding: 8px 4px 0;
  }
  .list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 0 8px 8px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .group {
    font-size: 12px;
    font-weight: 700;
    color: var(--dim);
    padding: 12px 8px 4px;
  }
  .item {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 9px 10px;
    border: none;
    border-radius: 8px;
    background: transparent;
    text-align: left;
  }
  .item.selected {
    background: var(--hover);
  }
  .line {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
  }
  .title {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .when {
    font-size: 12px;
    color: var(--dim);
    flex: none;
  }
  .snippet {
    font-size: 12px;
    color: var(--dim);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    width: 100%;
  }
  mark {
    background: var(--mark);
    color: var(--mark-text);
    border-radius: 3px;
  }
  .none {
    color: var(--dim);
    font-size: 13px;
    padding: 12px 8px;
  }
  section {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 52px;
    flex: none;
    padding: 0 10px 0 20px;
    border-bottom: 1px solid var(--border);
  }
  h1 {
    flex: 1;
    min-width: 0;
    margin: 0;
    font-size: 14px;
    font-weight: 700;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .btn.small {
    flex: none;
    height: 32px;
    padding: 0 12px;
    font-size: 13px;
  }
  .preview {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px 22px;
  }
  .bubble {
    overflow-wrap: anywhere;
    align-self: flex-end;
    max-width: 80%;
    background: var(--hover);
    border-radius: 14px;
    padding: 10px 14px;
    line-height: 1.45;
    white-space: pre-wrap;
    user-select: text;
  }
  .fallback {
    align-self: flex-start;
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
  .empty {
    flex: 1;
    display: grid;
    place-items: center;
    color: var(--dim);
  }
  button:disabled { opacity: 0.5; cursor: default; }
</style>
