<script lang="ts">
  import { onMount, tick } from "svelte";
  import { app } from "../lib/state.svelte";
  import { events, voiceApi } from "../lib/api";
  import { VoiceSession } from "../lib/voice.svelte";
  import Icon from "../components/Icon.svelte";
  import Chips from "../components/Chips.svelte";

  let engineName = $state("Transcription");
  const session = new VoiceSession({
    ...voiceApi,
    start: async (id) => {
      await voiceApi.start(id);
      engineName = await voiceApi.modelName();
    },
    onLevel: events.voiceLevel,
    send: async (text) => {
      if (app.streaming) throw new Error("Stop the current answer before sending the transcript.");
      app.input = text;
      await app.send(text);
      if (app.error) throw new Error(app.error);
    },
  });
  const phase = $derived(session.phase);
  let textarea: HTMLTextAreaElement | undefined = $state();

  function onKeydown(e: KeyboardEvent) {
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
    if (e.key === "Enter" && e.target instanceof Element && e.target.closest("button")) return;
    if (phase === "listening") {
      if (e.key === "Enter") {
        e.preventDefault();
        void session.stop("send");
      } else if (e.key === "Tab") {
        e.preventDefault();
        void session.stop("edit");
      }
    } else if (phase === "review" && e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void session.send();
    }
  }

  const clock = $derived(`${Math.floor(session.seconds / 60)}:${String(session.seconds % 60).padStart(2, "0")}`);

  onMount(() => {
    void session.start();
    const offHidden = events.hidden(() => {
      void session.dispose();
      app.back();
    });
    return () => {
      offHidden();
      void session.dispose();
    };
  });
  $effect(() => {
    if (phase === "review") void tick().then(() => textarea?.focus());
  });
</script>

<svelte:window onkeydown={onKeydown} />

<div class="top">
  <span class="logo"><Icon name="logo" size={24} stroke={1.6} /></span>
  {#if phase === "review"}
    <strong>Transcribed</strong><span class="dim">Edit before sending</span>
  {:else if phase === "transcribing" || phase === "sending"}
    <strong>{phase === "sending" ? "Sending" : "Transcribing"}</strong>
  {:else if phase === "starting"}
    <strong>Starting microphone</strong>
  {:else if phase === "error"}
    <strong>Dictation stopped</strong>
  {:else}
    <strong>Listening</strong><span class="clock">{clock}</span>
  {/if}
  <span class="engine">{engineName}</span>
</div>

{#if phase === "listening" || phase === "starting" || phase === "transcribing"}
  <div class="meter" class:busy={phase !== "listening"}>
    <div class="mic">
      <span class="ring"></span>
      <span class="dot"><Icon name="mic" size={24} stroke={1.9} /></span>
    </div>
    <div class="input-level" role="meter" aria-label="Microphone input activity" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(session.level * 100)} aria-valuetext={session.levelAvailable ? `${Math.round(session.level * 100)}% input activity` : "Waiting for input levels"}>
      <div class="bars" aria-hidden="true">
        {#each session.levels as level, i (i)}
          <span style="height: {4 + Math.round(level * 56)}px"></span>
        {/each}
      </div>
      <meter class="reduced-meter" aria-hidden="true" min="0" max="1" value={session.level}>Microphone activity</meter>
      {#if phase === "listening" && !session.levelAvailable}
        <span class="level-note">Waiting for input levels</span>
      {/if}
    </div>
  </div>
{:else if phase === "review"}
  <div class="review">
    <label for="transcript" class="sr-only">Transcript</label>
    <textarea id="transcript" rows="3" bind:this={textarea} bind:value={session.transcript}></textarea>
    {#if session.problem}<p class="error" role="alert">{session.problem}</p>{/if}
  </div>
{:else if phase === "sending"}
  <p class="dim" role="status">Sending your transcript...</p>
{:else}
  <p class="error" role="alert">{session.problem}</p>
{/if}

<Chips showAutoAttach={false} />

<div class="actions">
  {#if phase === "listening" || phase === "starting"}
    <button type="button" class="btn flat" onclick={() => app.back()}>Cancel<kbd>Esc</kbd></button>
    <button type="button" class="btn" disabled={phase !== "listening"} onclick={() => session.stop("edit")}>Stop and edit<kbd>Tab</kbd></button>
    <button type="button" class="btn primary" disabled={phase !== "listening"} onclick={() => session.stop("send")}>Stop and send<kbd>Enter</kbd></button>
  {:else if phase === "review"}
    <button type="button" class="btn flat" style="margin-right: auto" onclick={() => session.start()}>
      <Icon name="refresh" size={16} stroke={1.9} />Record again
    </button>
    <button type="button" class="btn primary" disabled={!session.transcript.trim()} onclick={() => session.send()}>
      Send<kbd>Enter</kbd>
    </button>
  {:else if phase === "error"}
    <button type="button" class="btn flat" style="margin-right: auto" onclick={() => app.back()}>Back</button>
    <button type="button" class="btn primary" onclick={() => session.start()}>Try again</button>
  {:else}
    <button type="button" class="btn flat" onclick={() => app.back()}>Cancel<kbd>Esc</kbd></button>
  {/if}
</div>

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 16px 18px;
    font-size: 16px;
  }
  .logo {
    color: var(--accent-text);
    display: flex;
  }
  .dim {
    font-size: 13px;
    color: var(--dim);
  }
  .clock {
    font-family: var(--mono);
    font-size: 13px;
    color: var(--dim);
  }
  .engine {
    margin-left: auto;
    height: 26px;
    padding: 0 10px;
    border-radius: 13px;
    background: var(--hover);
    color: var(--dim);
    font-size: 12px;
    display: inline-flex;
    align-items: center;
  }
  .meter {
    display: flex;
    align-items: center;
    gap: 22px;
    padding: 8px 24px 20px;
  }
  .mic {
    position: relative;
    width: 68px;
    height: 68px;
    flex: none;
  }
  .ring {
    position: absolute;
    inset: 0;
    border-radius: 34px;
    background: var(--rec-tint);
  }
  .dot {
    position: absolute;
    inset: 10px;
    border-radius: 24px;
    background: var(--rec);
    color: #ffffff;
    display: grid;
    place-items: center;
  }
  .input-level {
    flex: 1;
    min-width: 0;
  }
  .bars {
    height: 64px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .bars span {
    width: 5px;
    border-radius: 3px;
    background: var(--rec);
    transform-origin: center;
  }
  .busy .bars span {
    opacity: 0.35;
  }
  .level-note {
    color: var(--dim);
    font-size: 12px;
  }
  .reduced-meter {
    display: none;
    width: 100%;
    height: 16px;
  }
  @media (prefers-reduced-motion: reduce) {
    .bars { display: none; }
    .reduced-meter { display: block; }
  }
  .review {
    padding: 4px 18px 6px;
  }
  textarea {
    width: 100%;
    resize: none;
    border: 1px solid var(--accent);
    border-radius: 10px;
    padding: 12px 14px;
    background: var(--view);
    font-size: 18px;
    line-height: 1.45;
    outline: none;
    user-select: text;
  }
  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 8px;
    padding: 12px 14px;
    background: var(--foot);
    border-top: 1px solid var(--border);
  }
  .btn:disabled {
    opacity: 0.5;
  }
</style>
