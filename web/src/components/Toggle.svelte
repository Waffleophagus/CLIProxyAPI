<script lang="ts">
  // Accessible switch: a checkbox with role="switch".
  let {
    checked = $bindable(false),
    label,
    hint = '',
    disabled = false,
    onchange
  }: { checked?: boolean; label: string; hint?: string; disabled?: boolean; onchange?: (v: boolean) => void } = $props();
  const id = `t-${Math.random().toString(36).slice(2, 9)}`;
</script>

<div class="toggle-row">
  <div class="text">
    <label for={id}>{label}</label>
    {#if hint}<div class="hint">{hint}</div>{/if}
  </div>
  <input {id} type="checkbox" role="switch" class="switch" bind:checked {disabled} onchange={() => onchange?.(checked)} />
</div>

<style>
  .toggle-row {
    display: flex;
    align-items: flex-start;
    gap: 16px;
    justify-content: space-between;
  }
  .text {
    min-width: 0;
  }
  .hint {
    color: var(--text-muted);
    font-size: 0.86rem;
  }
  .switch {
    appearance: none;
    flex: none;
    width: 36px !important;
    height: 20px !important;
    border-radius: 999px;
    background: var(--baseline);
    position: relative;
    cursor: pointer;
    transition: background 0.15s;
    margin-top: 2px !important;
  }
  .switch::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: #fff;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
    transition: transform 0.15s;
  }
  .switch:checked {
    background: var(--accent);
  }
  .switch:checked::after {
    transform: translateX(16px);
  }
  .switch:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  @media (prefers-reduced-motion: reduce) {
    .switch,
    .switch::after {
      transition: none;
    }
  }
</style>
