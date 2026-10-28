<script lang="ts">
  let {
    checked,
    label,
    onchange,
  }: { checked: boolean; label: string; onchange: (v: boolean) => void } = $props();
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  class="switch"
  class:on={checked}
  onclick={() => onchange(!checked)}
>
  <span class="knob"></span>
</button>

<style>
  .switch {
    position: relative;
    width: 44px;
    height: 26px;
    flex: none;
    padding: 0;
    border: none;
    border-radius: 13px;
    background: var(--border-strong);
    transition: background 120ms;
  }
  .switch.on {
    background: var(--accent);
  }
  .knob {
    position: absolute;
    top: 3px;
    left: 3px;
    width: 20px;
    height: 20px;
    border-radius: 10px;
    background: #ffffff;
    transition: transform 120ms;
  }
  .on .knob {
    transform: translateX(18px);
  }
  @media (prefers-reduced-motion: reduce) {
    .switch, .knob { transition: none; }
  }
</style>
