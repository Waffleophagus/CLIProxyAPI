<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '../lib/api';
  import { config, saveConfig, type ConfigSnapshot } from '../lib/config.svelte';
  import { confirm } from '../lib/confirm.svelte';
  import { relative, dateTime } from '../lib/format';
  import { poll } from '../lib/poll';
  import { slugHealth, clientBaseURL } from '../lib/slugs';
  import { toasts } from '../lib/toast.svelte';
  import type { CompatGroup, CompatModel, SlugStatus } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import Icon from '../components/Icon.svelte';
  import Banner from '../components/Banner.svelte';
  import SlugDialog, { type SlugDraft } from './slugs/SlugDialog.svelte';
  import EndpointDialog, { type EndpointDraft } from './slugs/EndpointDialog.svelte';
  import SnippetDialog from './slugs/SnippetDialog.svelte';

  let statuses = $state<SlugStatus[]>([]);
  let loadError = $state('');
  let checking = $state<string | null>(null);

  let slugOpen = $state(false);
  let editing = $state<{ group: number; model: number } | null>(null);
  let endpointOpen = $state(false);
  let endpointIndex = $state(-1);
  let snippetOpen = $state(false);
  let snippetAlias = $state('');
  // Config version the open dialog was based on. Its indexes refer to this
  // version, and the write carries its ETag, so a change made elsewhere in
  // the meantime surfaces as a conflict instead of being overwritten.
  let editBase: ConfigSnapshot | null = null;

  const groups = $derived<CompatGroup[]>(config.data?.['api-keys']?.['openai-compatibility'] ?? []);

  async function load() {
    try {
      const r = await api.get<{ slugs: SlugStatus[] }>('routing/slugs');
      statuses = r.slugs ?? [];
      loadError = '';
    } catch (e) {
      loadError = errorMessage(e);
    }
  }

  onMount(() => {
    void load();
    void config.ensure();
    return poll(load, 15_000);
  });

  async function recheck(group?: string) {
    checking = group ?? '*';
    try {
      const r = await api.post<{ slugs: SlugStatus[] }>('routing/slugs/refresh', undefined, { query: { group } });
      statuses = r.slugs ?? [];
      const failed = statuses.filter((s) => (!group || s.group === group) && s.last_error);
      if (failed.length) toasts.error(`Check failed: ${failed[0].last_error}`);
      else toasts.ok(group ? `${group} checked` : 'All endpoints checked');
    } catch (e) {
      toasts.error(errorMessage(e));
    } finally {
      checking = null;
    }
  }

  function statusFor(group: string, alias: string): SlugStatus | undefined {
    return statuses.find((s) => s.group === group && s.alias === alias);
  }

  function groupStatus(group: string): SlugStatus | undefined {
    const list = statuses.filter((s) => s.group === group);
    return list.find((s) => s.last_check_at) ?? list[0];
  }

  function groupsOf(snap: ConfigSnapshot): CompatGroup[] {
    return snap.doc['api-keys']?.['openai-compatibility'] ?? [];
  }

  async function writeGroups(snap: ConfigSnapshot, next: CompatGroup[], okText: string, refreshGroup?: string): Promise<boolean> {
    const ok = await saveConfig(() => config.put('api-keys/openai-compatibility', next, snap), okText);
    if (ok) {
      // Give the hot reload a moment, then re-poll so the new slug resolves.
      setTimeout(async () => {
        if (refreshGroup) {
          try {
            const r = await api.post<{ slugs: SlugStatus[] }>('routing/slugs/refresh', undefined, { query: { group: refreshGroup } });
            statuses = r.slugs ?? [];
          } catch {
            void load();
          }
        } else void load();
      }, 800);
    }
    return ok;
  }

  function openNew() {
    editBase = config.snapshot();
    if (!editBase) return;
    editing = null;
    slugOpen = true;
  }

  function openEdit(gi: number, mi: number) {
    editBase = config.snapshot();
    if (!editBase) return;
    editing = { group: gi, model: mi };
    slugOpen = true;
  }

  function openEndpoint(gi: number) {
    editBase = config.snapshot();
    if (!editBase) return;
    endpointIndex = gi;
    endpointOpen = true;
  }

  async function saveSlug(d: SlugDraft): Promise<boolean> {
    const snap = editBase;
    if (!snap) return false;
    const next = groupsOf(snap);
    let gi = d.groupIndex;
    if (gi < 0) {
      const g: CompatGroup = {
        name: d.newName.trim(),
        'base-url': d.newBaseURL.trim(),
        keys: d.newKey.trim() ? [{ 'api-key': d.newKey.trim() }] : [],
        models: []
      };
      next.push(g);
      gi = next.length - 1;
    }
    const g = next[gi];
    g.models = g.models ?? [];
    const model: CompatModel = editing ? { ...g.models[editing.model] } : {};
    model.alias = d.alias.trim();
    if (d.mode === 'follow') {
      model.follow = true;
      delete model.name;
      if (d.match.trim()) model.match = d.match.trim();
      else delete model.match;
    } else {
      model.name = d.pinned.trim();
      delete model.follow;
      delete model.match;
    }
    if (editing) g.models[editing.model] = model;
    else g.models.push(model);
    return writeGroups(snap, next, editing ? `Slug ${model.alias} updated` : `Slug ${model.alias} created`, g.name);
  }

  async function deleteSlug(gi: number, mi: number) {
    const snap = config.snapshot();
    if (!snap) return;
    const next = groupsOf(snap);
    const g = next[gi];
    const m = g.models?.[mi];
    const ok = await confirm({
      title: 'Delete slug?',
      message: `Clients requesting "${m?.alias || m?.name}" will get an error. The endpoint "${g.name}" stays configured.`,
      confirmLabel: 'Delete slug',
      danger: true
    });
    if (!ok) return;
    g.models = (g.models ?? []).filter((_, i) => i !== mi);
    await writeGroups(snap, next, 'Slug deleted');
  }

  async function deleteEndpoint(gi: number) {
    const snap = config.snapshot();
    if (!snap) return;
    const g = groupsOf(snap)[gi];
    const ok = await confirm({
      title: `Delete endpoint ${g.name}?`,
      message: `This removes the endpoint and its ${g.models?.length ?? 0} slug(s) from the configuration.`,
      confirmLabel: 'Delete endpoint',
      danger: true
    });
    if (!ok) return;
    const next = groupsOf(snap).filter((_, i) => i !== gi);
    await writeGroups(snap, next, `Endpoint ${g.name} deleted`);
  }

  async function saveEndpoint(d: EndpointDraft): Promise<boolean> {
    const snap = editBase;
    if (!snap) return false;
    const next = groupsOf(snap);
    const g = next[endpointIndex];
    g.name = d.name.trim();
    g['base-url'] = d.baseURL.trim();
    if (d.interval.trim()) g['follow-interval'] = d.interval.trim();
    else delete g['follow-interval'];
    if (d.disabled) g.disabled = true;
    else delete g.disabled;
    const keys = (g.keys ?? []).map((k) => {
      const copy = { ...k };
      delete copy.auth_index;
      return copy;
    });
    if (d.keyAction === 'replace') {
      if (keys.length) keys[0] = { ...keys[0], 'api-key': d.key.trim() };
      else keys.push({ 'api-key': d.key.trim() });
    } else if (d.keyAction === 'remove') {
      keys.shift();
    }
    g.keys = keys;
    delete g.auth_index;
    return writeGroups(snap, next, `Endpoint ${g.name} saved`, d.disabled ? undefined : g.name);
  }

  function showSnippet(alias: string) {
    snippetAlias = alias;
    snippetOpen = true;
  }
</script>

<PageHeader
  title="Slugs"
  subtitle="Stable model names for your own OpenAI-compatible servers. Clients ask for the slug; the proxy sends the request to the model behind it."
>
  {#snippet actions()}
    <button class="btn" type="button" onclick={() => recheck()} disabled={checking !== null || groups.length === 0}>
      <Icon name="refresh" />{checking === '*' ? 'Checking…' : 'Re-check all'}
    </button>
    <button class="btn primary" type="button" onclick={openNew} disabled={!config.loaded}><Icon name="plus" />New slug</button>
  {/snippet}
</PageHeader>

{#if loadError}<div class="gap"><Banner tone="bad">{loadError}</Banner></div>{/if}

<div class="card card-pad gap usebox">
  <div>
    <strong>Client base URL</strong>
    <p class="muted small">Use with any client key from <em>Client keys</em>; set the model to a slug below.</p>
  </div>
  <div class="row"><code>{clientBaseURL()}</code><CopyButton text={clientBaseURL()} label="Copy base URL" /></div>
</div>

{#if config.data === null}
  <p class="muted">{config.error ? `Could not load the configuration: ${config.error}` : 'Loading…'}</p>
{:else if groups.length === 0}
  <div class="card empty-state">
    <h2>No endpoints yet</h2>
    <p class="muted">
      Add an OpenAI-compatible server (for example a DGX Spark running vLLM or TensorFold) and give its model a stable slug.
    </p>
    <button class="btn primary" type="button" onclick={openNew}><Icon name="plus" />New slug</button>
  </div>
{:else}
  <div class="stack">
    {#each groups as g, gi (g.name + gi)}
      {@const gs = groupStatus(g.name)}
      {@const polls = (g.models ?? []).some((m) => m.follow && !m.name)}
      <section class="card endpoint" aria-labelledby="ep-{gi}">
        <header class="ep-head">
          <div class="ep-title">
            <div class="row wrap">
              <h2 id="ep-{gi}">{g.name}</h2>
              {#if g.disabled}
                <StatusBadge tone="neutral" text="Disabled" />
              {:else if gs && polls}
                {@const h = slugHealth(gs)}
                <StatusBadge
                  tone={h.tone === 'warn' && gs.last_success_at ? 'good' : h.tone}
                  text={gs.last_error ? 'Unreachable' : gs.last_success_at ? 'Reachable' : 'Not checked'}
                />
              {/if}
            </div>
            <div class="row wrap small muted">
              <code>{g['base-url']}</code>
              {#if g.keys?.length}<span>· key set</span>{/if}
              {#if gs?.last_check_at && polls}<span title={dateTime(gs.last_check_at)}
                  >· checked {relative(gs.last_check_at)}{gs.poll_interval ? `, every ${gs.poll_interval}` : ''}</span
                >{/if}
            </div>
          </div>
          <div class="row wrap">
            {#if polls && !g.disabled}
              <button class="btn sm" type="button" onclick={() => recheck(g.name)} disabled={checking !== null}
                ><Icon name="refresh" size={14} />{checking === g.name ? 'Checking…' : 'Re-check now'}</button
              >
            {/if}
            <button class="btn sm" type="button" onclick={() => openEndpoint(gi)}><Icon name="edit" size={14} />Edit</button>
            <button
              class="btn sm ghost danger icon"
              type="button"
              aria-label="Delete endpoint {g.name}"
              title="Delete endpoint"
              onclick={() => deleteEndpoint(gi)}><Icon name="trash" size={14} /></button
            >
          </div>
        </header>
        {#if gs?.last_error && polls}
          <div class="pad"><Banner tone="bad" title="Last check failed">{gs.last_error}</Banner></div>
        {/if}
        {#if gs?.reported_models?.length}
          <div class="pad small reported">
            <span class="muted">Endpoint reports:</span>
            {#each gs.reported_models as m (m)}<code class="chip">{m}</code>{/each}
          </div>
        {/if}
        {#if !(g.models ?? []).length}
          <p class="pad muted">No slugs on this endpoint.</p>
        {:else}
          <div class="table-wrap">
            <table class="data">
              <thead>
                <tr
                  ><th>Slug</th><th>Mode</th><th>Upstream model</th><th class="hide-sm">Match</th><th
                    ><span class="sr-only">Actions</span></th
                  ></tr
                >
              </thead>
              <tbody>
                {#each g.models ?? [] as m, mi (mi)}
                  {@const st = statusFor(g.name, m.alias || m.name || '')}
                  {@const follow = m.follow === true && !m.name}
                  <tr>
                    <td><strong>{m.alias || m.name}</strong></td>
                    <td>
                      {#if follow}<StatusBadge tone="info" text="Follow" title="Uses whatever the endpoint serves" />{:else}<StatusBadge
                          tone="neutral"
                          text="Pinned"
                        />{/if}
                    </td>
                    <td>
                      {#if follow}
                        {#if st?.resolved_model}<code>{st.resolved_model}</code>{:else}<span class="warn-text"
                            >waiting for first successful check</span
                          >{/if}
                      {:else}
                        <code>{m.name}</code>
                      {/if}
                    </td>
                    <td class="hide-sm"
                      >{#if m.match}<code>{m.match}</code>{:else}<span class="muted">–</span>{/if}</td
                    >
                    <td class="actions">
                      <button class="btn sm ghost" type="button" onclick={() => showSnippet(m.alias || m.name || '')}>Use</button>
                      <button
                        class="btn sm ghost icon"
                        type="button"
                        aria-label="Edit {m.alias}"
                        title="Edit"
                        onclick={() => openEdit(gi, mi)}><Icon name="edit" size={14} /></button
                      >
                      <button
                        class="btn sm ghost danger icon"
                        type="button"
                        aria-label="Delete {m.alias}"
                        title="Delete"
                        onclick={() => deleteSlug(gi, mi)}><Icon name="trash" size={14} /></button
                      >
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>
    {/each}
  </div>
  <p class="path foot">api-keys.openai-compatibility</p>
{/if}

<SlugDialog bind:open={slugOpen} {groups} {statuses} {editing} onsave={saveSlug} />
<EndpointDialog
  bind:open={endpointOpen}
  group={endpointIndex >= 0 ? groups[endpointIndex] : null}
  taken={groups.map((g) => g.name)}
  onsave={saveEndpoint}
/>
<SnippetDialog bind:open={snippetOpen} alias={snippetAlias} />

<style>
  .gap {
    margin-bottom: 16px;
  }
  .usebox {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .empty-state {
    padding: 40px 24px;
    text-align: center;
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
  }
  .ep-head {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    padding: 14px 18px 10px;
  }
  .ep-title {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .pad {
    padding: 0 18px 10px;
  }
  .reported {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .chip {
    background: var(--surface-2);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .warn-text {
    color: var(--warn-text);
  }
  table.data th:first-child,
  table.data td:first-child {
    padding-left: 18px;
  }
  table.data td:last-child {
    padding-right: 12px;
  }
  .foot {
    margin-top: 12px;
  }
  @media (max-width: 640px) {
    .hide-sm {
      display: none;
    }
  }
</style>
