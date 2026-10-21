<script lang="ts">
  import { toasts } from '../lib/toast.svelte';
  import Icon from './Icon.svelte';
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each toasts.items as t (t.id)}
    <div class="toast {t.kind}">
      <Icon name={t.kind === 'ok' ? 'ok' : t.kind === 'error' ? 'alert' : 'info'} />
      <span class="text">{t.text}</span>
      <button class="btn ghost sm icon" type="button" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}
        ><Icon name="x" size={14} /></button
      >
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: 16px;
    bottom: 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    z-index: 1000;
    max-width: min(420px, calc(100vw - 32px));
  }
  .toast {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 8px 10px 12px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-left-width: 4px;
    border-radius: 8px;
    box-shadow: var(--shadow-pop);
  }
  .toast :global(svg) {
    margin-top: 2px;
  }
  .text {
    flex: 1;
  }
  .ok {
    border-left-color: var(--good);
  }
  .ok > :global(svg) {
    color: var(--good-text);
  }
  .error {
    border-left-color: var(--bad);
  }
  .error > :global(svg) {
    color: var(--bad-text);
  }
  .info {
    border-left-color: var(--accent);
  }
</style>
