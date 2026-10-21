<script lang="ts">
  import { mask } from '../lib/format';
  import CopyButton from './CopyButton.svelte';
  import Icon from './Icon.svelte';

  let { value, label = 'secret' }: { value: string; label?: string } = $props();
  let shown = $state(false);
</script>

<span class="secret">
  <code class={['value', { revealed: shown }]}>{shown ? value : mask(value)}</code>
  <button
    type="button"
    class="btn ghost sm icon"
    onclick={() => (shown = !shown)}
    aria-label={shown ? `Hide ${label}` : `Reveal ${label}`}
    title={shown ? 'Hide' : 'Reveal'}
  >
    <Icon name={shown ? 'eyeOff' : 'eye'} size={14} />
  </button>
  <CopyButton text={value} label={`Copy ${label}`} />
</span>

<style>
  .secret {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
    max-width: 100%;
  }
  .value {
    padding: 2px 6px;
    background: var(--surface-2);
    border-radius: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .value.revealed {
    white-space: normal;
    word-break: break-all;
  }
</style>
