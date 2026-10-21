<script lang="ts">
  import { confirmState } from '../lib/confirm.svelte';
  import Dialog from './Dialog.svelte';

  // Writable derived: the dialog may set it to false (Escape, backdrop); it
  // follows confirmState again on the next request.
  let open = $derived(confirmState.current !== null);

  function finish(ok: boolean) {
    const req = confirmState.current;
    confirmState.current = null;
    req?.resolve(ok);
  }
</script>

<Dialog bind:open title={confirmState.current?.title ?? 'Confirm'} onclose={() => finish(false)}>
  <p class="msg">{confirmState.current?.message}</p>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => finish(false)}>Cancel</button>
    <button class="btn {confirmState.current?.danger ? 'danger solid' : 'primary'}" type="button" onclick={() => finish(true)}>
      {confirmState.current?.confirmLabel ?? 'Confirm'}
    </button>
  {/snippet}
</Dialog>

<style>
  .msg {
    white-space: pre-line;
    color: var(--text-2);
  }
</style>
