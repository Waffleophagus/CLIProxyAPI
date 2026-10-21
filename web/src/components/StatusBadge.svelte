<script lang="ts" module>
  export type Tone = 'good' | 'warn' | 'bad' | 'neutral' | 'info';
</script>

<script lang="ts">
  import Icon from './Icon.svelte';

  // Status always pairs color with an icon and a word, never color alone.
  let { tone = 'neutral', text, title = '' }: { tone?: Tone; text: string; title?: string } = $props();
  const icons: Record<Tone, string> = { good: 'ok', warn: 'clock', bad: 'error', neutral: 'pause', info: 'info' };
</script>

<span class="badge {tone}" title={title || undefined}>
  <Icon name={icons[tone]} size={13} />
  <span>{text}</span>
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 1px 8px 1px 6px;
    border-radius: 999px;
    font-size: 0.8rem;
    font-weight: 500;
    line-height: 20px;
    white-space: nowrap;
    border: 1px solid transparent;
  }
  .good {
    background: var(--good-soft);
    color: var(--good-text);
  }
  .warn {
    background: var(--warn-soft);
    color: var(--warn-text);
  }
  .bad {
    background: var(--bad-soft);
    color: var(--bad-text);
  }
  .neutral {
    background: var(--surface-2);
    color: var(--text-2);
  }
  .info {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
</style>
