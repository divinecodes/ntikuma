<script lang="ts">
  import type { ApprovalEvent } from "../lib/types";

  let { approval, busy = false, error = "", focusTick = 0, decide }:
    { approval: ApprovalEvent; busy?: boolean; error?: string; focusTick?: number; decide: (allow: boolean) => void } = $props();
  let denyButton: HTMLButtonElement | undefined = $state();

  $effect(() => {
    void focusTick;
    denyButton?.focus({ preventScroll: true });
  });
</script>

<section class="command-approval" role="group" aria-label="Command approval" aria-busy={busy}>
  <h2>Run this command?</h2>
  <textarea class="command-text" aria-label="Exact command" readonly spellcheck={false} rows={Math.min(6, Math.max(2, approval.command.split("\n").length))} value={approval.command}></textarea>
  <p>Runs in <span class="directory">{approval.workingDirectory}</span>. Commands can change files outside your tool folders. Output is sent to your model through OpenRouter.</p>
  <p>Approval expires after two minutes.</p>
  {#if error}<p class="approval-error" role="alert">{error}</p>{/if}
  <div class="actions">
    <span><kbd>Esc</kbd> Deny</span>
    <button type="button" class="btn" aria-label="Deny command" bind:this={denyButton} disabled={busy} onclick={() => decide(false)}>Deny</button>
    <button type="button" class="btn primary" aria-label="Allow command" disabled={busy} onclick={() => decide(true)}>Allow once</button>
  </div>
</section>

<style>
  .command-approval { border: 1px solid var(--border-strong); border-radius: 10px; padding: 14px; background: var(--view); }
  h2 { margin: 0 0 10px; font-size: 14px; }
  .command-text { display: block; width: 100%; margin: 0 0 10px; padding: 10px; border: none; border-radius: 6px; background: var(--hover); color: var(--fg); max-height: 160px; resize: vertical; overflow: auto; user-select: text; font: 13px var(--mono); tab-size: 4; }
  p { margin: 6px 0; font-size: 12px; color: var(--dim); line-height: 1.4; }
  .directory { overflow-wrap: anywhere; }
  .approval-error { color: var(--red); }
  .actions { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
  .actions span { margin-right: auto; color: var(--dim); font-size: 12px; }
  button:disabled { opacity: 0.5; cursor: default; }
</style>
