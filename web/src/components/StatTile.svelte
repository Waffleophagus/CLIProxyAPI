<script lang="ts">
  let { label, value, sub = '', tone = '' }: { label: string; value: string; sub?: string; tone?: '' | 'bad' | 'warn' } = $props();
</script>

<div class="tile card">
  <div class="label">{label}</div>
  <div class="value">{value}</div>
  {#if sub}<div class={['sub', { bad: tone === 'bad', warn: tone === 'warn' }]}>{sub}</div>{/if}
</div>

<style>
  .tile {
    padding: 14px 16px;
    min-width: 0;
  }
  .label {
    color: var(--text-muted);
    font-size: 0.86rem;
  }
  .value {
    font-size: 1.6rem;
    font-weight: 600;
    line-height: 1.3;
    margin-top: 2px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    color: var(--text-muted);
    font-size: 0.82rem;
  }
  .sub.bad {
    color: var(--bad-text);
  }
  .sub.warn {
    color: var(--warn-text);
  }
</style>
