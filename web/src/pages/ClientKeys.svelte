<script lang="ts">
  import { onMount } from 'svelte';
  import { errorMessage } from '../lib/api';
  import { config, isExampleKey, saveConfig, type ConfigSnapshot } from '../lib/config.svelte';
  import { confirm } from '../lib/confirm.svelte';
  import { clientKeyId, generateKey } from '../lib/crypto';
  import { compact, int, mask, pct, relative, dateTime } from '../lib/format';
  import { clientBaseURL } from '../lib/slugs';
  import { UsageDisabledError, usageRequests, usageSummary, windowFor } from '../lib/usage';
  import type { UsageGroup } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import Secret from '../components/Secret.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import Dialog from '../components/Dialog.svelte';
  import Banner from '../components/Banner.svelte';
  import Icon from '../components/Icon.svelte';

  let stats = $state<Record<string, UsageGroup>>({});
  let lastUsed = $state<Record<string, string>>({});
  let usageOff = $state(false);
  let usageError = $state('');
  let newKey = $state('');
  let newOpen = $state(false);
  let saving = $state(false);

  const keys = $derived(config.data?.['access']?.['api-keys'] ?? []);
  const examples = $derived(keys.filter(isExampleKey));

  async function loadUsage() {
    try {
      const w = windowFor('30d');
      const s = await usageSummary({ from: w.from, to: w.to }, 'api_key');
      const map: Record<string, UsageGroup> = {};
      for (const g of s.groups ?? []) map[g.key] = g;
      stats = map;
      usageOff = false;
      // Last use per key (one small query each; few keys in practice).
      for (const k of keys) {
        const id = clientKeyId(k);
        if (!id || !map[id] || lastUsed[id]) continue;
        const r = await usageRequests({ api_key: id }, 1);
        if (r.requests[0]) lastUsed = { ...lastUsed, [id]: r.requests[0].ts };
      }
    } catch (e) {
      if (e instanceof UsageDisabledError) usageOff = true;
      else usageError = errorMessage(e);
    }
  }

  onMount(() => {
    void config.ensure().then(loadUsage);
  });

  function openGenerate() {
    newKey = generateKey();
    newOpen = true;
  }

  // Every change is computed from one config snapshot and sent with that
  // snapshot's ETag; nothing can be written before the config has loaded.
  function keysOf(snap: ConfigSnapshot): string[] {
    return snap.doc.access?.['api-keys'] ?? [];
  }

  async function addKey() {
    const snap = config.snapshot();
    if (!snap) return;
    saving = true;
    // Adding a real key also drops the example keys, which turns safe mode off.
    const current = keysOf(snap);
    const replacing = current.some(isExampleKey);
    const next = [...current.filter((k) => !isExampleKey(k)), newKey];
    const ok = await saveConfig(
      () => config.put('access/api-keys', next, snap),
      replacing ? 'Example keys replaced; safe mode is off' : 'Client key added'
    );
    saving = false;
    if (ok) newOpen = false;
  }

  async function revoke(key: string) {
    const snap = config.snapshot();
    if (!snap) return;
    const current = keysOf(snap);
    const last = current.length === 1;
    const ok = await confirm({
      title: 'Revoke client key?',
      message: last
        ? `Clients using ${mask(key)} will be rejected.\n\nThis is the last key: with no client keys, the proxy accepts requests from anyone who can reach it.`
        : `Clients using ${mask(key)} will be rejected immediately.`,
      confirmLabel: 'Revoke key',
      danger: true
    });
    if (!ok) return;
    await saveConfig(
      () =>
        config.put(
          'access/api-keys',
          current.filter((k) => k !== key),
          snap
        ),
      'Client key revoked'
    );
  }

  async function removeExamples() {
    const snap = config.snapshot();
    if (!snap) return;
    await saveConfig(
      () =>
        config.put(
          'access/api-keys',
          keysOf(snap).filter((k) => !isExampleKey(k)),
          snap
        ),
      'Example keys removed'
    );
  }
</script>

<PageHeader title="Client keys" subtitle="Keys your apps use to call this proxy (Authorization: Bearer …). These are not provider keys.">
  {#snippet actions()}
    <button class="btn primary" type="button" onclick={openGenerate} disabled={!config.loaded}><Icon name="plus" />Generate key</button>
  {/snippet}
</PageHeader>

<div class="stack">
  {#if examples.length > 0}
    <Banner tone="bad" title="Replace the example keys">
      <p>
        The example keys ({examples.join(', ')}) are public. While they are configured at startup the proxy runs in safe mode and refuses
        all requests.
      </p>
      <div class="row wrap" style="margin-top:8px">
        <button class="btn primary" type="button" onclick={openGenerate} disabled={!config.loaded}>Generate a real key</button>
        {#if examples.length < keys.length}
          <button class="btn" type="button" onclick={removeExamples}>Just remove the example keys</button>
        {/if}
      </div>
    </Banner>
  {/if}
  {#if keys.length === 0 && config.data}
    <Banner tone="warn" title="No client keys">
      Anyone who can reach this proxy can use it. Generate a key to require authentication.
    </Banner>
  {/if}
  {#if usageError}<Banner tone="bad">{usageError}</Banner>{/if}

  <section class="card" aria-label="Client keys">
    {#if config.data === null}
      <p class="empty">{config.error ? `Could not load the configuration: ${config.error}` : 'Loading…'}</p>
    {:else if keys.length === 0}
      <p class="empty">No keys yet.</p>
    {:else}
      <div class="table-wrap">
        <table class="data">
          <thead>
            <tr>
              <th>Key</th>
              <th class="r">Requests · 30d</th>
              <th class="r">Success</th>
              <th class="r">Tokens · 30d</th>
              <th>Last used</th>
              <th><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each keys as k (k)}
              {@const id = clientKeyId(k)}
              {@const s = stats[id]}
              <tr>
                <td>
                  <div class="row">
                    <Secret value={k} label="client key" />
                    {#if isExampleKey(k)}<span class="ex">example</span>{/if}
                  </div>
                  <span class="small muted">id {id}</span>
                </td>
                <td class="r">{usageOff ? '–' : int(s?.requests ?? 0)}</td>
                <td class="r">{s ? pct(s.successes, s.requests) : '–'}</td>
                <td class="r">{s ? compact(s.total_tokens) : '–'}</td>
                <td title={lastUsed[id] ? dateTime(lastUsed[id]) : ''}
                  >{lastUsed[id] ? relative(lastUsed[id]) : s ? '…' : 'not in 30 days'}</td
                >
                <td class="r">
                  <button class="btn sm ghost danger" type="button" onclick={() => revoke(k)}>Revoke</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
  {#if usageOff}<p class="small muted">Usage tracking is off, so per-key numbers are not available.</p>{/if}
  <p class="path">access.api-keys</p>
</div>

<Dialog bind:open={newOpen} title="New client key">
  <div class="stack">
    <p class="muted">A random 40-character key generated in your browser. Copy it now; you can reveal it again on this page later.</p>
    <div class="newkey">
      <code>{newKey}</code>
      <CopyButton text={newKey} label="Copy key" showLabel small={false} />
    </div>
    <div class="small muted">
      Use it with base URL <code>{clientBaseURL()}</code> as <code>Authorization: Bearer {mask(newKey)}</code>.
    </div>
    {#if examples.length}
      <Banner tone="info">Saving also removes the example keys and turns safe mode off.</Banner>
    {/if}
    <button class="btn ghost sm regen" type="button" onclick={() => (newKey = generateKey())}
      ><Icon name="refresh" size={14} />Generate another</button
    >
  </div>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (newOpen = false)}>Cancel</button>
    <button class="btn primary" type="button" onclick={addKey} disabled={saving || !config.loaded}>{saving ? 'Saving…' : 'Save key'}</button
    >
  {/snippet}
</Dialog>

<style>
  table.data th:first-child,
  table.data td:first-child {
    padding-left: 18px;
  }
  table.data td:last-child {
    padding-right: 12px;
  }
  .ex {
    font-size: 0.75rem;
    font-weight: 600;
    padding: 0 6px;
    border-radius: 4px;
    background: var(--bad-soft);
    color: var(--bad-text);
  }
  .newkey {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 10px 12px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    word-break: break-all;
  }
  .newkey code {
    flex: 1;
    font-size: 0.95rem;
  }
  .regen {
    align-self: flex-start;
  }
</style>
