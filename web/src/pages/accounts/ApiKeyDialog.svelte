<script lang="ts" module>
  export type ApiKeyDraft = { groupIndex: number; name: string; baseURL: string; key: string };
</script>

<script lang="ts">
  import Dialog from '../../components/Dialog.svelte';
  import Banner from '../../components/Banner.svelte';
  import type { ProviderGroup } from '../../lib/types';

  let {
    open = $bindable(false),
    providerLabel,
    defaultBase,
    groups,
    initialGroup = -1,
    onsave
  }: {
    open?: boolean;
    providerLabel: string;
    defaultBase: string;
    groups: ProviderGroup[];
    initialGroup?: number;
    onsave: (d: ApiKeyDraft) => Promise<boolean>;
  } = $props();

  let d = $state<ApiKeyDraft>({ groupIndex: -1, name: '', baseURL: '', key: '' });
  let error = $state('');
  let saving = $state(false);

  $effect(() => {
    if (!open) return;
    error = '';
    d = { groupIndex: initialGroup >= 0 ? initialGroup : groups.length ? 0 : -1, name: '', baseURL: '', key: '' };
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!d.key.trim()) return (error = 'Enter the API key.');
    if (d.groupIndex < 0) {
      if (!d.name.trim()) return (error = 'Give the group a name.');
      if (d.baseURL.trim() && !/^https?:\/\/\S+$/.test(d.baseURL.trim())) return (error = 'Base URL must start with http:// or https://');
    }
    saving = true;
    const ok = await onsave($state.snapshot(d));
    saving = false;
    if (ok) open = false;
  }
</script>

<Dialog bind:open title="Add {providerLabel} API key">
  <form id="apikey-form" class="stack" onsubmit={submit}>
    {#if error}<Banner tone="bad">{error}</Banner>{/if}
    <div class="field">
      <label for="ak-group">Group</label>
      <select id="ak-group" bind:value={d.groupIndex}>
        {#each groups as g, i (i)}
          <option value={i}>{g.name || `group ${i + 1}`} — {g['base-url'] || defaultBase}</option>
        {/each}
        <option value={-1}>New group…</option>
      </select>
      <span class="hint">Keys in one group share a base URL and settings, and are used in rotation.</span>
    </div>
    {#if d.groupIndex < 0}
      <div class="field">
        <label for="ak-name">Group name</label>
        <input id="ak-name" type="text" bind:value={d.name} placeholder={providerLabel.toLowerCase()} autocomplete="off" />
      </div>
      <div class="field">
        <label for="ak-base">Base URL <span class="muted">(optional)</span></label>
        <input id="ak-base" type="url" bind:value={d.baseURL} placeholder={defaultBase} autocomplete="off" />
        <span class="hint">Leave empty for the official API.</span>
      </div>
    {/if}
    <div class="field">
      <label for="ak-key">API key</label>
      <input id="ak-key" type="password" bind:value={d.key} autocomplete="off" />
    </div>
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="apikey-form" disabled={saving}>{saving ? 'Saving…' : 'Add key'}</button>
  {/snippet}
</Dialog>
