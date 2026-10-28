<script lang="ts">
  import type { ToolActivity as Activity } from "../lib/types";

  let { activity = [] }: { activity?: Activity[] } = $props();
</script>

{#if activity.length}
  <ul class="tool-activity" aria-label="Tool activity">
    {#each activity as item (item.callId)}
      <li class:failed={item.status === "error"}>
        <span class="summary">{item.summary}</span>
        {#if item.status === "waiting"}<span class="state">Awaiting approval</span>
        {:else if item.status === "started"}<span class="state">Running</span>
        {:else if item.status === "error"}<span class="state">Failed</span>
        {:else if item.status === "stopped"}<span class="state">Stopped</span>
        {:else if item.status === "denied"}<span class="state">Denied</span>{/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .tool-activity {
    margin: 0;
    padding: 0;
    list-style: none;
    color: var(--dim);
    font-size: 12px;
  }
  li {
    display: flex;
    gap: 8px;
    align-items: baseline;
    padding: 2px 0;
  }
  .summary { overflow-wrap: anywhere; min-width: 0; }
  .state { flex: none; font-size: 11px; }
  .failed .state { color: var(--red); }
</style>
