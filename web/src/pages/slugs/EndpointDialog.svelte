<script lang="ts" module>
  export type EndpointDraft = {
    name: string;
    baseURL: string;
    key: string;
    keyAction: 'keep' | 'replace' | 'remove';
    interval: string;
    disabled: boolean;
  };
</script>

<script lang="ts">
  import Dialog from '../../components/Dialog.svelte';
  import Banner from '../../components/Banner.svelte';
  import Toggle from '../../components/Toggle.svelte';
  import { mask } from '../../lib/format';
  import type { CompatGroup } from '../../lib/types';

  let {
    open = $bindable(false),
    group,
    taken,
    onsave
  }: { open?: boolean; group: CompatGroup | null; taken: string[]; onsave: (d: EndpointDraft) => Promise<boolean> } = $props();

  let d = $state<EndpointDraft>({ name: '', baseURL: '', key: '', keyAction: 'keep', interval: '', disabled: false });
  let error = $state('');
  let saving = $state(false);
  const currentKey = $derived(group?.keys?.[0]?.['api-key'] ?? '');
  const extraKeys = $derived(Math.max(0, (group?.keys?.length ?? 0) - 1));

  $effect(() => {
    if (!open || !group) return;
    error = '';
    d = {
      name: group.name,
      baseURL: group['base-url'],
      key: '',
      keyAction: 'keep',
      interval: group['follow-interval'] ?? '',
      disabled: group.disabled === true
    };
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(d.name.trim())) return (error = 'Name: use letters, numbers, dots, dashes or underscores.');
    if (d.name.trim() !== group?.name && taken.includes(d.name.trim())) return (error = 'Another endpoint already uses that name.');
    if (!/^https?:\/\/\S+$/.test(d.baseURL.trim())) return (error = 'Base URL must start with http:// or https://');
    if (d.interval.trim() && !/^\d+(\.\d+)?(ms|s|m|h)$/.test(d.interval.trim()))
      return (error = 'Check interval looks like 30s, 1m or 2h.');
    if (d.keyAction === 'replace' && !d.key.trim()) return (error = 'Enter the new API key.');
    saving = true;
    const ok = await onsave($state.snapshot(d));
    saving = false;
    if (ok) open = false;
  }
</script>

<Dialog bind:open title="Edit endpoint">
  <form id="ep-form" class="stack" onsubmit={submit}>
    {#if error}<Banner tone="bad">{error}</Banner>{/if}
    <div class="field">
      <label for="ed-name">Name</label>
      <input id="ed-name" type="text" bind:value={d.name} autocomplete="off" />
      {#if group && d.name.trim() !== group.name}
        <span class="hint">Renaming starts a new provider id (<code>openai-compatible-{d.name.trim()}</code>) in usage history.</span>
      {/if}
    </div>
    <div class="field">
      <label for="ed-url">Base URL</label>
      <input id="ed-url" type="url" bind:value={d.baseURL} autocomplete="off" />
    </div>
    <div class="field">
      <span class="label">API key</span>
      <div class="row wrap">
        {#if currentKey}<code>{mask(currentKey)}</code>{:else}<span class="muted">None</span>{/if}
        {#if extraKeys}<span class="muted small">+{extraKeys} more (edit in config.yaml)</span>{/if}
      </div>
      <div class="row wrap">
        <label class="opt"><input type="radio" bind:group={d.keyAction} value="keep" />Keep</label>
        <label class="opt"><input type="radio" bind:group={d.keyAction} value="replace" />{currentKey ? 'Replace' : 'Set'}</label>
        {#if currentKey}<label class="opt"><input type="radio" bind:group={d.keyAction} value="remove" />Remove</label>{/if}
      </div>
      {#if d.keyAction === 'replace'}
        <input type="password" bind:value={d.key} aria-label="New API key" autocomplete="off" />
      {/if}
    </div>
    <div class="field">
      <label for="ed-int">Check interval <span class="muted">(optional)</span></label>
      <input id="ed-int" type="text" bind:value={d.interval} placeholder="30s" autocomplete="off" />
      <span class="hint">How often follow-mode slugs re-read the model list (minimum 5s).</span>
    </div>
    <Toggle bind:checked={d.disabled} label="Disabled" hint="Stop routing to this endpoint without deleting it." />
    <p class="path">api-keys.openai-compatibility[name={group?.name}]</p>
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="ep-form" disabled={saving}>{saving ? 'Saving…' : 'Save endpoint'}</button>
  {/snippet}
</Dialog>

<style>
  .label {
    font-weight: 500;
  }
  .opt {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-weight: 400;
    margin-right: 10px;
  }
</style>
