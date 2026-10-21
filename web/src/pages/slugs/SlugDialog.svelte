<script lang="ts" module>
  export type SlugDraft = {
    groupIndex: number; // -1 = new endpoint
    newName: string;
    newBaseURL: string;
    newKey: string;
    alias: string;
    mode: 'follow' | 'pinned';
    match: string;
    pinned: string;
  };
</script>

<script lang="ts">
  import Dialog from '../../components/Dialog.svelte';
  import Banner from '../../components/Banner.svelte';
  import type { CompatGroup, CompatModel, SlugStatus } from '../../lib/types';

  let {
    open = $bindable(false),
    groups,
    statuses,
    editing = null,
    onsave
  }: {
    open?: boolean;
    groups: CompatGroup[];
    statuses: SlugStatus[];
    /** Group index and model index being edited, or null to create. */
    editing?: { group: number; model: number } | null;
    onsave: (draft: SlugDraft) => Promise<boolean>;
  } = $props();

  let draft = $state<SlugDraft>(blank());
  let saving = $state(false);
  let error = $state('');
  let pinOther = $state(false);

  function blank(): SlugDraft {
    return {
      groupIndex: groups.length ? 0 : -1,
      newName: '',
      newBaseURL: '',
      newKey: '',
      alias: '',
      mode: 'follow',
      match: '',
      pinned: ''
    };
  }

  // Reset the form each time the dialog opens.
  $effect(() => {
    if (!open) return;
    error = '';
    if (editing) {
      const g = groups[editing.group];
      const m: CompatModel = g?.models?.[editing.model] ?? {};
      const pinned = (m.name ?? '').trim();
      draft = {
        groupIndex: editing.group,
        newName: '',
        newBaseURL: '',
        newKey: '',
        alias: m.alias ?? '',
        mode: m.follow && !pinned ? 'follow' : 'pinned',
        match: m.match ?? '',
        pinned
      };
    } else {
      draft = blank();
    }
    pinOther = false;
  });

  const group = $derived(draft.groupIndex >= 0 ? groups[draft.groupIndex] : null);
  const reported = $derived(group ? (statuses.find((s) => s.group === group.name)?.reported_models ?? []) : []);
  $effect(() => {
    if (draft.mode === 'pinned' && draft.pinned && reported.length && !reported.includes(draft.pinned)) pinOther = true;
  });

  function validate(): string {
    if (draft.groupIndex < 0) {
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(draft.newName.trim()))
        return 'Endpoint name: use letters, numbers, dots, dashes or underscores.';
      if (groups.some((g) => g.name === draft.newName.trim())) return 'An endpoint with that name already exists.';
      if (!/^https?:\/\/\S+$/.test(draft.newBaseURL.trim())) return 'Base URL must start with http:// or https://';
    }
    const alias = draft.alias.trim();
    if (!alias) return 'Enter the slug clients will request.';
    if (/\s/.test(alias)) return 'The slug cannot contain spaces.';
    const clash = groups.some((g, gi) =>
      (g.models ?? []).some(
        (m, mi) => m.alias === alias && !(editing && editing.group === gi && editing.model === mi) && gi === draft.groupIndex
      )
    );
    if (clash) return 'This endpoint already has that slug.';
    if (draft.mode === 'pinned' && !draft.pinned.trim()) return 'Choose the upstream model to pin.';
    return '';
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = validate();
    if (error) return;
    saving = true;
    const ok = await onsave($state.snapshot(draft));
    saving = false;
    if (ok) open = false;
  }
</script>

<Dialog bind:open title={editing ? 'Edit slug' : 'New slug'}>
  <form id="slug-form" class="stack" onsubmit={submit}>
    {#if error}<Banner tone="bad">{error}</Banner>{/if}
    <div class="field">
      <label for="slug-endpoint">Endpoint</label>
      <select id="slug-endpoint" bind:value={draft.groupIndex} disabled={editing !== null}>
        {#each groups as g, i (g.name + i)}
          <option value={i}>{g.name} — {g['base-url']}</option>
        {/each}
        <option value={-1}>New endpoint…</option>
      </select>
      <span class="hint">An OpenAI-compatible server such as vLLM, SGLang, llama.cpp or TensorFold.</span>
    </div>

    {#if draft.groupIndex < 0}
      <fieldset class="stack sub">
        <legend>New endpoint</legend>
        <div class="field">
          <label for="ep-name">Name</label>
          <input id="ep-name" type="text" bind:value={draft.newName} placeholder="dgx-spark" autocomplete="off" />
        </div>
        <div class="field">
          <label for="ep-url">Base URL</label>
          <input id="ep-url" type="url" bind:value={draft.newBaseURL} placeholder="http://spark.local:8000/v1" autocomplete="off" />
          <span class="hint">Include <code>/v1</code>. The proxy polls <code>{'{base URL}'}/models</code>.</span>
        </div>
        <div class="field">
          <label for="ep-key">API key <span class="muted">(optional)</span></label>
          <input
            id="ep-key"
            type="password"
            bind:value={draft.newKey}
            autocomplete="off"
            placeholder="Leave empty for servers without auth"
          />
        </div>
      </fieldset>
    {/if}

    <div class="field">
      <label for="slug-alias">Slug</label>
      <input id="slug-alias" type="text" bind:value={draft.alias} placeholder="DGX-SPARK" autocomplete="off" />
      <span class="hint">The model name clients send. Keep it stable; the model behind it can change.</span>
    </div>

    <fieldset class="modes">
      <legend>Which model does it use?</legend>
      <label class="choice">
        <input type="radio" name="slug-mode" value="follow" bind:group={draft.mode} />
        <span
          ><strong>Follow the endpoint</strong><br /><span class="muted small"
            >Use whatever model the endpoint currently serves. Best for single-model servers.</span
          ></span
        >
      </label>
      <label class="choice">
        <input type="radio" name="slug-mode" value="pinned" bind:group={draft.mode} />
        <span><strong>Pin a model</strong><br /><span class="muted small">Always use one specific upstream model.</span></span>
      </label>
    </fieldset>

    {#if draft.mode === 'follow'}
      <div class="field">
        <label for="slug-match">Only match <span class="muted">(optional)</span></label>
        <input id="slug-match" type="text" bind:value={draft.match} placeholder="GLM-*" autocomplete="off" />
        <span class="hint">When the endpoint lists several models, use the first one matching this pattern (<code>*</code> wildcard).</span>
      </div>
    {:else}
      <div class="field">
        <label for="slug-pin">Upstream model</label>
        {#if reported.length && !pinOther}
          <select id="slug-pin" bind:value={draft.pinned}>
            <option value="" disabled>Choose a model…</option>
            {#each reported as m (m)}<option value={m}>{m}</option>{/each}
          </select>
          <button type="button" class="btn ghost sm other" onclick={() => (pinOther = true)}>Type a name instead</button>
        {:else}
          <input id="slug-pin" type="text" bind:value={draft.pinned} placeholder="GLM-5.3-Flash-EXL3" autocomplete="off" />
          {#if reported.length}
            <button type="button" class="btn ghost sm other" onclick={() => (pinOther = false)}>Pick from reported models</button>
          {:else}
            <span class="hint">The endpoint has not reported any models yet; type the exact model id.</span>
          {/if}
        {/if}
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="slug-form" disabled={saving}
      >{saving ? 'Saving…' : editing ? 'Save changes' : 'Create slug'}</button
    >
  {/snippet}
</Dialog>

<style>
  fieldset {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 12px 14px;
    margin: 0;
  }
  legend {
    font-weight: 500;
    padding: 0 4px;
  }
  .modes {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .choice {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    font-weight: 400;
    cursor: pointer;
  }
  .choice input {
    margin-top: 3px;
  }
  .other {
    align-self: flex-start;
  }
</style>
