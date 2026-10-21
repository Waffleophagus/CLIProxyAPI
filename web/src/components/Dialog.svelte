<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // Native <dialog> gives focus trapping, Escape and inert background for free.
  let {
    open = $bindable(false),
    title,
    variant = 'modal',
    wide = false,
    onclose,
    children,
    footer
  }: {
    open?: boolean;
    title: string;
    variant?: 'modal' | 'drawer';
    wide?: boolean;
    onclose?: () => void;
    children: Snippet;
    footer?: Snippet;
  } = $props();

  let el: HTMLDialogElement | undefined = $state();
  let wasOpen = false;
  const titleId = `dlg-${Math.random().toString(36).slice(2, 9)}`;

  // onclose fires once per close, whatever closed it (button, Escape,
  // backdrop click, or the parent setting open = false).
  $effect(() => {
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && wasOpen) {
      if (el.open) el.close();
      untrack(() => onclose?.());
    }
    wasOpen = open;
  });

  function handleClose() {
    open = false;
  }

  function backdrop(e: MouseEvent) {
    if (e.target === el) handleClose();
  }
</script>

<dialog bind:this={el} class={[variant, { wide }]} onclose={handleClose} onclick={backdrop} aria-labelledby={titleId}>
  {#if open}
    <div class="frame">
      <header>
        <h2 id={titleId}>{title}</h2>
        <button type="button" class="btn ghost icon" onclick={handleClose} aria-label="Close"><Icon name="x" /></button>
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    padding: 0;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-pop);
    max-height: calc(100dvh - 32px);
  }
  dialog::backdrop {
    background: rgba(0, 0, 0, 0.35);
  }
  dialog.modal {
    width: min(560px, calc(100vw - 24px));
    border-radius: 12px;
  }
  dialog.modal.wide {
    width: min(820px, calc(100vw - 24px));
  }
  dialog.drawer {
    margin: 0 0 0 auto;
    height: 100dvh;
    max-height: 100dvh;
    width: min(620px, 100vw);
    border-radius: 0;
    border-width: 0 0 0 1px;
  }
  .frame {
    display: flex;
    flex-direction: column;
    max-height: inherit;
    height: 100%;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 16px 10px 20px;
    border-bottom: 1px solid var(--border);
  }
  .body {
    padding: 16px 20px 20px;
    overflow: auto;
    flex: 1;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 20px;
    border-top: 1px solid var(--border);
    flex-wrap: wrap;
  }
</style>
