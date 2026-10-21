<script lang="ts">
  import { tick } from 'svelte';
  import { copyText } from '../lib/clipboard';
  import Icon from './Icon.svelte';

  let {
    text,
    label = 'Copy',
    showLabel = false,
    small = true
  }: { text: string; label?: string; showLabel?: boolean; small?: boolean } = $props();
  let done = $state(false);
  let manual = $state(false);
  let btn: HTMLButtonElement | undefined = $state();
  let field: HTMLTextAreaElement | undefined = $state();

  async function copy() {
    if (await copyText(text, btn)) {
      manual = false;
      done = true;
      setTimeout(() => (done = false), 1500);
      return;
    }
    // Copying is blocked (plain HTTP, permissions): show the text selected so
    // the user can copy it with the keyboard.
    manual = true;
    await tick();
    field?.focus();
    field?.select();
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      // Close the fallback without closing a surrounding dialog.
      e.preventDefault();
      e.stopPropagation();
      manual = false;
      btn?.focus();
    }
  }
</script>

<span class="copy">
  <button
    bind:this={btn}
    type="button"
    class={['btn ghost', { sm: small, icon: !showLabel }]}
    onclick={copy}
    title={label}
    aria-label={label}
  >
    <Icon name={done ? 'check' : 'copy'} size={14} />
    {#if showLabel}<span>{done ? 'Copied' : label}</span>{/if}
  </button>
  {#if manual}
    <span class="manual" role="group" aria-label="Copy manually">
      <span class="small">Copy failed. The text is selected: press Ctrl+C (⌘C on Mac).</span>
      <textarea
        bind:this={field}
        class="copy-manual"
        readonly
        rows={text.length > 80 ? 4 : 1}
        value={text}
        onkeydown={onKey}
        onfocus={(e) => e.currentTarget.select()}
        spellcheck="false"></textarea>
      <button type="button" class="btn sm" onclick={() => (manual = false)}>Close</button>
    </span>
  {/if}
</span>

<style>
  .copy {
    position: relative;
    display: inline-flex;
  }
  .manual {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    z-index: 20;
    display: flex;
    flex-direction: column;
    gap: 6px;
    width: min(360px, 80vw);
    padding: 10px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-pop);
    text-align: left;
    white-space: normal;
  }
  .manual textarea {
    width: 100%;
    font-family: var(--mono);
    font-size: 0.85rem;
    resize: vertical;
  }
  .manual .btn {
    align-self: flex-end;
  }
</style>
