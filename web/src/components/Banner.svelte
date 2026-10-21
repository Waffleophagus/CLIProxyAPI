<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  let { tone = 'info', title = '', children }: { tone?: 'info' | 'warn' | 'bad' | 'good'; title?: string; children: Snippet } = $props();
  const icon = { info: 'info', warn: 'alert', bad: 'alert', good: 'ok' };
</script>

<div class="banner {tone}" role={tone === 'bad' ? 'alert' : undefined}>
  <Icon name={icon[tone]} size={18} />
  <div class="content">
    {#if title}<strong>{title}</strong>{/if}
    <div>{@render children()}</div>
  </div>
</div>

<style>
  .banner {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 14px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
  }
  .banner > :global(svg) {
    margin-top: 1px;
  }
  .content {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    flex: 1;
  }
  .info {
    background: var(--accent-soft);
  }
  .info > :global(svg) {
    color: var(--accent-text);
  }
  .warn {
    background: var(--warn-soft);
  }
  .warn > :global(svg) {
    color: var(--warn-text);
  }
  .bad {
    background: var(--bad-soft);
  }
  .bad > :global(svg) {
    color: var(--bad-text);
  }
  .good {
    background: var(--good-soft);
  }
  .good > :global(svg) {
    color: var(--good-text);
  }
</style>
