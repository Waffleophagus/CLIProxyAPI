<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '../lib/api';
  import { config, saveConfig, type ConfigSnapshot } from '../lib/config.svelte';
  import { confirm } from '../lib/confirm.svelte';
  import { credentialHealth, accountTitle, reasonText } from '../lib/accounts';
  import { int, mask, providerName, relative, shortDateTime, dateTime } from '../lib/format';
  import { link, router } from '../lib/router.svelte';
  import { poll } from '../lib/poll';
  import { toasts } from '../lib/toast.svelte';
  import type { Credential, ProviderGroup } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import Icon from '../components/Icon.svelte';
  import Banner from '../components/Banner.svelte';
  import LoginDialog, { OAUTH_PROVIDERS } from './accounts/LoginDialog.svelte';
  import ApiKeyDialog, { type ApiKeyDraft } from './accounts/ApiKeyDialog.svelte';

  type KeyCounters = Record<string, Record<string, { success: number; failed: number }>>;

  const API_KEY_PROVIDERS = [
    { id: 'claude', label: 'Claude', base: 'https://api.anthropic.com' },
    { id: 'codex', label: 'Codex', base: 'https://chatgpt.com/backend-api/codex' },
    { id: 'xai', label: 'xAI', base: 'https://api.x.ai/v1' },
    { id: 'meta', label: 'Meta', base: 'https://api.meta.ai/v1' }
  ] as const;
  type ApiKeyProvider = (typeof API_KEY_PROVIDERS)[number]['id'];

  const ORDER = ['claude', 'codex', 'xai', 'meta'];

  let creds = $state<Credential[] | null>(null);
  let counters = $state<KeyCounters>({});
  let loadError = $state('');
  let busy = $state<Record<string, boolean>>({});
  // Per credential: model ids, 'loading', or an error message.
  let expanded = $state<Record<string, string[] | string>>({});

  let loginOpen = $state(false);
  let loginProvider = $state('');
  let siwcName = $state('');
  let keyOpen = $state(false);
  let keyProvider = $state<ApiKeyProvider>('claude');
  let keyGroup = $state(-1);
  // Config version the add-key dialog was opened on; its group indexes and
  // ETag belong to that version.
  let keyBase: ConfigSnapshot | null = null;

  async function load() {
    try {
      const [c, k] = await Promise.all([
        api.get<{ files: Credential[] }>('credentials'),
        api.get<KeyCounters>('observability/usage/api-keys').catch(() => ({}))
      ]);
      creds = c.files ?? [];
      counters = k;
      loadError = '';
    } catch (e) {
      loadError = errorMessage(e);
    }
  }

  onMount(() => {
    void load();
    void config.ensure();
    return poll(load, 20_000);
  });

  const grouped = $derived.by(() => {
    const byProvider: Record<string, Credential[]> = {};
    for (const c of creds ?? []) {
      const p = (c.provider || c.type || 'other').toLowerCase();
      (byProvider[p] ??= []).push(c);
    }
    return Object.entries(byProvider).sort((a, b) => {
      const ia = ORDER.indexOf(a[0]);
      const ib = ORDER.indexOf(b[0]);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a[0].localeCompare(b[0]);
    });
  });

  const apiKeyGroups = $derived(
    API_KEY_PROVIDERS.map((p) => ({ ...p, groups: config.data?.['api-keys']?.[p.id] ?? [] })).filter((p) => p.groups.length > 0)
  );
  const compatCount = $derived(config.data?.['api-keys']?.['openai-compatibility']?.length ?? 0);

  async function act(id: string, fn: () => Promise<unknown>, okText: string) {
    busy = { ...busy, [id]: true };
    try {
      await fn();
      toasts.ok(okText);
      await load();
    } catch (e) {
      toasts.error(errorMessage(e));
    } finally {
      busy = { ...busy, [id]: false };
    }
  }

  function toggle(c: Credential) {
    const disable = !c.disabled;
    void act(
      c.id,
      () => api.patch('credentials/status', { name: c.name, auth_index: c.auth_index, disabled: disable }),
      disable ? 'Account disabled' : 'Account enabled'
    );
  }

  function refreshToken(c: Credential) {
    void act(c.id, () => api.post('credentials/refresh', { name: c.name, auth_index: c.auth_index }), 'Token refreshed');
  }

  function resetCooldown(c: Credential) {
    void act(c.id, () => api.post('routing/cooldown/reset', { auth_index: c.auth_index }), 'Cooldown cleared');
  }

  async function remove(c: Credential) {
    const ok = await confirm({
      title: 'Remove account?',
      message: `${accountTitle(c)} (${providerName(c.provider)}) will be signed out and its credential file deleted. You can add it again later.`,
      confirmLabel: 'Remove account',
      danger: true
    });
    if (ok) void act(c.id, () => api.del('credentials', { query: { name: c.name } }), 'Account removed');
  }

  async function toggleModels(c: Credential) {
    if (expanded[c.id]) {
      const { [c.id]: _, ...rest } = expanded;
      expanded = rest;
      return;
    }
    expanded = { ...expanded, [c.id]: 'loading' };
    try {
      const r = await api.get<{ models: { id: string }[] }>('credentials/models', { query: { name: c.name } });
      expanded = { ...expanded, [c.id]: r.models.map((m) => m.id) };
    } catch (e) {
      expanded = { ...expanded, [c.id]: errorMessage(e) };
    }
  }

  function addAccount(provider = '') {
    siwcName = '';
    loginProvider = provider;
    loginOpen = true;
  }

  function bindSIWC(c: Credential) {
    loginProvider = '';
    siwcName = c.name;
    loginOpen = true;
  }

  function keyStats(provider: string, key: string) {
    const bucket = counters[provider] ?? {};
    let success = 0;
    let failed = 0;
    let found = false;
    for (const [composite, v] of Object.entries(bucket)) {
      if (composite.endsWith('|' + key)) {
        success += v.success;
        failed += v.failed;
        found = true;
      }
    }
    return found ? { success, failed } : null;
  }

  function providerGroups(snap: ConfigSnapshot, p: ApiKeyProvider): ProviderGroup[] {
    return snap.doc['api-keys']?.[p] ?? [];
  }

  function openAddKey(p: ApiKeyProvider, gi = -1) {
    keyBase = config.snapshot();
    if (!keyBase) return;
    keyProvider = p;
    keyGroup = gi;
    keyOpen = true;
  }

  async function saveKey(d: ApiKeyDraft): Promise<boolean> {
    const snap = keyBase;
    if (!snap) return false;
    const list = providerGroups(snap, keyProvider);
    if (d.groupIndex < 0) {
      const g: ProviderGroup = { name: d.name.trim(), keys: [{ 'api-key': d.key.trim() }] };
      if (d.baseURL.trim()) g['base-url'] = d.baseURL.trim();
      list.push(g);
    } else {
      const g = list[d.groupIndex];
      g.keys = [...(g.keys ?? []), { 'api-key': d.key.trim() }];
    }
    return saveConfig(() => config.put(`api-keys/${keyProvider}`, list, snap), 'API key added');
  }

  async function removeKey(p: ApiKeyProvider, gi: number, ki: number) {
    const snap = config.snapshot();
    if (!snap) return;
    const list = providerGroups(snap, p);
    const g = list[gi];
    const key = g.keys?.[ki]?.['api-key'] ?? '';
    const last = (g.keys?.length ?? 0) <= 1;
    const ok = await confirm({
      title: 'Remove API key?',
      message: last
        ? `${mask(key)} is the only key in group "${g.name || gi + 1}". Removing it also removes the group.`
        : `Requests will stop using ${mask(key)}.`,
      confirmLabel: 'Remove key',
      danger: true
    });
    if (!ok) return;
    if (last) list.splice(gi, 1);
    else g.keys = (g.keys ?? []).filter((_, i) => i !== ki);
    await saveConfig(() => config.put(`api-keys/${p}`, list, snap), 'API key removed');
  }

  const keyDialogProvider = $derived(API_KEY_PROVIDERS.find((p) => p.id === keyProvider)!);
</script>

<PageHeader title="Accounts" subtitle="Upstream accounts the proxy uses: signed-in subscriptions and provider API keys.">
  {#snippet actions()}
    <button class="btn" type="button" onclick={() => openAddKey('claude')} disabled={!config.loaded}><Icon name="key" />Add API key</button>
    <button class="btn primary" type="button" onclick={() => addAccount()}><Icon name="plus" />Add account</button>
  {/snippet}
</PageHeader>

{#if loadError}<div class="gap"><Banner tone="bad">{loadError}</Banner></div>{/if}

{#if creds === null}
  <p class="muted">Loading…</p>
{:else}
  <div class="stack">
    {#if grouped.length === 0}
      <div class="card empty-state">
        <h2>No signed-in accounts</h2>
        <p class="muted">Sign in with a Claude, Codex, xAI or Meta account to route requests through it.</p>
        <div class="row wrap center">
          {#each OAUTH_PROVIDERS as p (p.id)}
            <button class="btn" type="button" onclick={() => addAccount(p.id)}>{p.label}</button>
          {/each}
        </div>
      </div>
    {/if}

    {#each grouped as [provider, list] (provider)}
      <section class="card" aria-labelledby="prov-{provider}">
        <header class="sec-head">
          <h2 id="prov-{provider}">{providerName(provider)}</h2>
          <span class="muted small">{list.length} account{list.length === 1 ? '' : 's'}</span>
          <div class="spacer"></div>
          {#if OAUTH_PROVIDERS.some((p) => p.id === provider)}
            <button class="btn sm" type="button" onclick={() => addAccount(provider)}><Icon name="plus" size={14} />Add</button>
          {/if}
        </header>
        <ul class="accounts">
          {#each list as c (c.id)}
            {@const h = credentialHealth(c)}
            {@const models = expanded[c.id]}
            <li class={{ off: c.disabled }}>
              <div class="acct">
                <div class="who">
                  <div class="row wrap">
                    <strong class="truncate">{accountTitle(c)}</strong>
                    <StatusBadge tone={h.tone} text={h.text} />
                    {#if c.use_siwc}<StatusBadge tone="info" text="SIWC" title="Uses Sign in with ChatGPT credentials" />{/if}
                  </div>
                  <div class="small muted meta">
                    {#if h.detail}<span class={{ 'warn-text': h.tone === 'warn', 'bad-text': h.tone === 'bad' }}>{h.detail}</span>{/if}
                    <span>{int(c.success)} ok · {int(c.failed)} failed</span>
                    {#if c.account_type}<span>{c.account_type}</span>{/if}
                    {#if c.last_refresh}<span title={dateTime(c.last_refresh)}>refreshed {relative(c.last_refresh)}</span>{/if}
                    <span class="path" title="Credential file">{c.name}</span>
                  </div>
                  {#if c.cooldowns?.length}
                    <ul class="cds small">
                      {#each c.cooldowns as cd, i (i)}
                        <li>
                          {cd.scope === 'model' ? cd.model_key : 'Whole account'}: {reasonText(cd.reason)}, until {shortDateTime(
                            cd.retry_at
                          )}
                        </li>
                      {/each}
                    </ul>
                  {/if}
                </div>
                <div class="acts">
                  <label class="sw" title={c.disabled ? 'Enable account' : 'Disable account'}>
                    <input
                      type="checkbox"
                      role="switch"
                      checked={!c.disabled}
                      disabled={busy[c.id]}
                      onchange={() => toggle(c)}
                      aria-label="Use {accountTitle(c)}"
                    />
                    <span class="small">{c.disabled ? 'Off' : 'On'}</span>
                  </label>
                  {#if c.cooldowns?.length || h.category === 'cooling'}
                    <button class="btn sm" type="button" onclick={() => resetCooldown(c)} disabled={busy[c.id]}>Reset cooldown</button>
                  {/if}
                  {#if h.category === 'relogin' && OAUTH_PROVIDERS.some((p) => p.id === provider)}
                    <button class="btn sm primary" type="button" onclick={() => addAccount(provider)}>Sign in again</button>
                  {/if}
                  <button class="btn sm ghost" type="button" onclick={() => toggleModels(c)} aria-expanded={!!models}>Models</button>
                  <button
                    class="btn sm ghost icon"
                    type="button"
                    onclick={() => refreshToken(c)}
                    disabled={busy[c.id]}
                    aria-label="Refresh token for {accountTitle(c)}"
                    title="Refresh token"><Icon name="refresh" size={14} /></button
                  >
                  {#if c.siwc_bind_supported}
                    <button class="btn sm ghost" type="button" onclick={() => bindSIWC(c)} title="Bind Sign in with ChatGPT"
                      >{c.siwc_bound ? 'Rebind SIWC' : 'Bind SIWC'}</button
                    >
                  {/if}
                  <button
                    class="btn sm ghost danger icon"
                    type="button"
                    onclick={() => remove(c)}
                    disabled={busy[c.id]}
                    aria-label="Remove {accountTitle(c)}"
                    title="Remove"><Icon name="trash" size={14} /></button
                  >
                </div>
              </div>
              {#if models}
                <div class="models small">
                  {#if models === 'loading'}
                    <span class="muted">Loading models…</span>
                  {:else if typeof models === 'string'}
                    <span class="bad-text">{models}</span>
                  {:else if models.length === 0}
                    <span class="muted">No models registered for this account.</span>
                  {:else}
                    {#each models as m (m)}<code class="chip">{m}</code>{/each}
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/each}

    <section class="card" aria-labelledby="apikeys-h">
      <header class="sec-head">
        <h2 id="apikeys-h">Provider API keys</h2>
        <div class="spacer"></div>
      </header>
      {#if config.data === null}
        <p class="pad muted">{config.error ? `Could not load the configuration: ${config.error}` : 'Loading…'}</p>
      {:else if apiKeyGroups.length === 0}
        <p class="pad muted">
          No provider API keys. Add one for Claude, Codex, xAI or Meta if you pay per token instead of using a subscription.
        </p>
        <div class="pad row wrap">
          {#each API_KEY_PROVIDERS as p (p.id)}
            <button class="btn sm" type="button" onclick={() => openAddKey(p.id)}><Icon name="plus" size={14} />{p.label}</button>
          {/each}
        </div>
      {:else}
        {#each apiKeyGroups as p (p.id)}
          <div class="kp">
            <div class="row">
              <h3>{p.label}</h3>
              <div class="spacer"></div>
              <button class="btn sm" type="button" onclick={() => openAddKey(p.id)}><Icon name="plus" size={14} />Add key</button>
            </div>
            {#each p.groups as g, gi (gi)}
              <div class="kgroup">
                <div class="small muted">
                  {g.name || `Group ${gi + 1}`} · <code>{g['base-url'] || p.base}</code>{#if g.prefix}
                    · prefix <code>{g.prefix}/</code>{/if}
                </div>
                <ul class="keys">
                  {#each g.keys ?? [] as k, ki (ki)}
                    {@const st = keyStats(p.id, k['api-key'] ?? '')}
                    <li>
                      <code>{mask(k['api-key'] ?? '')}</code>
                      <span class="small muted"
                        >{st ? `${int(st.success)} ok · ${int(st.failed)} failed since start` : 'no requests yet'}</span
                      >
                      <div class="spacer"></div>
                      <button
                        class="btn sm ghost danger icon"
                        type="button"
                        onclick={() => removeKey(p.id, gi, ki)}
                        aria-label="Remove key {mask(k['api-key'] ?? '')}"
                        title="Remove key"><Icon name="trash" size={14} /></button
                      >
                    </li>
                  {/each}
                </ul>
              </div>
            {/each}
            <p class="path">api-keys.{p.id}</p>
          </div>
        {/each}
        <div class="pad row wrap small muted">
          Add keys for:
          {#each API_KEY_PROVIDERS.filter((p) => !apiKeyGroups.some((g) => g.id === p.id)) as p (p.id)}
            <button class="btn sm ghost" type="button" onclick={() => openAddKey(p.id)}>{p.label}</button>
          {/each}
        </div>
      {/if}
    </section>

    <section class="card card-pad">
      <div class="row wrap">
        <div>
          <h2>OpenAI-compatible endpoints</h2>
          <p class="muted small">{compatCount} endpoint{compatCount === 1 ? '' : 's'} configured (your own servers such as a DGX Spark).</p>
        </div>
        <div class="spacer"></div>
        <a class="btn" href={router.href('/slugs')} use:link>Manage on the Slugs page</a>
      </div>
    </section>
  </div>
{/if}

<LoginDialog bind:open={loginOpen} provider={loginProvider} {siwcName} ondone={load} />
<ApiKeyDialog
  bind:open={keyOpen}
  providerLabel={keyDialogProvider.label}
  defaultBase={keyDialogProvider.base}
  groups={config.data?.['api-keys']?.[keyProvider] ?? []}
  initialGroup={keyGroup}
  onsave={saveKey}
/>

<style>
  .gap {
    margin-bottom: 16px;
  }
  .empty-state {
    padding: 32px 24px;
    text-align: center;
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
  }
  .center {
    justify-content: center;
  }
  .sec-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 18px 10px;
    border-bottom: 1px solid var(--border);
  }
  .accounts,
  .keys,
  .cds {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .accounts > li {
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
  }
  .accounts > li:last-child {
    border-bottom: none;
  }
  .accounts > li.off .who {
    opacity: 0.7;
  }
  .acct {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    justify-content: space-between;
    flex-wrap: wrap;
  }
  .who {
    min-width: 0;
    flex: 1 1 320px;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    margin-top: 2px;
  }
  .cds {
    margin-top: 4px;
    color: var(--warn-text);
  }
  .acts {
    display: flex;
    gap: 4px;
    align-items: center;
    flex-wrap: wrap;
  }
  .sw {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-right: 6px;
    font-weight: 400;
    cursor: pointer;
  }
  .sw input {
    appearance: none;
    width: 32px !important;
    height: 18px !important;
    border-radius: 999px;
    background: var(--baseline);
    position: relative;
    cursor: pointer;
  }
  .sw input::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: #fff;
    transition: transform 0.15s;
  }
  .sw input:checked {
    background: var(--accent);
  }
  .sw input:checked::after {
    transform: translateX(14px);
  }
  .models {
    margin-top: 8px;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    background: var(--surface-2);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .warn-text {
    color: var(--warn-text);
  }
  .bad-text {
    color: var(--bad-text);
  }
  .pad {
    padding: 12px 18px;
  }
  .kp {
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
  }
  .kgroup {
    margin: 8px 0;
  }
  .keys li {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 4px 0;
  }
</style>
