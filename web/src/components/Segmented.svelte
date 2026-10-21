<script lang="ts" generics="T extends string">
  let {
    value = $bindable(),
    options,
    label,
    onchange
  }: { value: T; options: { value: T; label: string }[]; label: string; onchange?: (v: T) => void } = $props();
  const name = `seg-${Math.random().toString(36).slice(2, 8)}`;
</script>

<div class="seg" role="radiogroup" aria-label={label}>
  {#each options as o (o.value)}
    <label class={{ on: value === o.value }}>
      <input
        type="radio"
        {name}
        value={o.value}
        checked={value === o.value}
        onchange={() => {
          value = o.value;
          onchange?.(o.value);
        }}
      />
      <span>{o.label}</span>
    </label>
  {/each}
</div>

<style>
  .seg {
    display: inline-flex;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    padding: 2px;
    gap: 2px;
  }
  label {
    position: relative;
    font-weight: 500;
    font-size: 0.86rem;
    padding: 3px 10px;
    border-radius: 4px;
    cursor: pointer;
    color: var(--text-2);
  }
  label.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.12);
  }
  input {
    position: absolute;
    opacity: 0;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  label:has(input:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
  }
</style>
