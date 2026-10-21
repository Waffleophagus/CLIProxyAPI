<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '../../lib/api';
  import { config, saveConfig } from '../../lib/config.svelte';
  import { compact, int, perM, providerName, relative, shortDateTime, usd } from '../../lib/format';
  import { toasts } from '../../lib/toast.svelte';
  import { pricedModels, pricingStatus, refreshPricing, unpricedLocalModels } from '../../lib/usage';
  import type { CostClass, Credential, PricedModel, PricingStatus, UsagePriceModel } from '../../lib/types';
  import Dialog from '../../components/Dialog.svelte';
  import ModelPicker from '../../components/ModelPicker.svelte';
  import Banner from '../../components/Banner.svelte';
  import Icon from '../../components/Icon.svelte';

  let {
    open = $bindable(false),
    range,
    focus = '',
    onsaved
  }: {
    open?: boolean;
    /** Window whose models are listed. */
    range: { from: string; to: string };
    /** Model ID whose price editor opens on load. */
    focus?: string;
    onsaved?: () => void;
  } = $props();

  const CLASS_LABELS: Record<CostClass, string> = { api: 'Paid API', subscription: 'Subscription', local: 'My hardware' };

  let status = $state<PricingStatus | null>(null);
  let models = $state<PricedModel[]>([]);
  let creds = $state<Credential[]>([]);
  let loading = $state(false);
  let error = $state('');

  type Draft = {
    match: string;
    mode: 'catalog' | 'custom';
    priceAs: string;
    input: string | number | null;
    output: string | number | null;
    cacheRead: string | number | null;
    cacheWrite: string | number | null;
  };
  let editing = $state<string | null>(null);
  let editingModel = $state<PricedModel | null>(null);
  // How a changed price applies to a Spark model that already has one: asked every time.
  let applyMode = $state<'' | 'all' | 'from-now'>('');
  let draft = $state<Draft>({ match: '', mode: 'catalog', priceAs: '', input: '', output: '', cacheRead: '', cacheWrite: '' });
  let fees = $state<Record<string, string | number | null>>({});
  let classes = $state<Record<string, '' | CostClass>>({});

  const pricing = $derived(config.data?.observability?.usage?.pricing ?? {});
  const overrides = $derived(pricing.models ?? []);
  const subscriptionCreds = $derived(creds.filter((c) => c.account_type !== 'api_key'));
  const orphanFees = $derived((pricing.subscriptions ?? []).filter((s) => !creds.some((c) => c.id === s.account)));
  const providers = $derived([...new Set(models.map((m) => m.provider))].sort());
  const unpricedLocal = $derived(models.filter((m) => m.class === 'local' && !m.priced_as));
  // Spark (local) prices stick to the served model, so a price change can be
  // limited to new requests; other prices always apply to the whole history.
  const needsApplyChoice = $derived(editingModel?.class === 'local' && entriesFor(draft.match).length > 0);

  // The parent mounts this dialog only while it is open, so each opening loads fresh data.
  onMount(() => void load());

  function rowKey(m: PricedModel): string {
    return `${m.provider}|${m.requested_model}|${m.upstream_model}|${m.class}|${m.priced_as ?? ''}`;
  }

  /** Price entries for a model ID, oldest first. */
  function entriesFor(model: string): UsagePriceModel[] {
    const id = model.trim().toLowerCase();
    return overrides.filter((o) => o.model.toLowerCase() === id).sort((a, b) => (a.from ?? '').localeCompare(b.from ?? ''));
  }

  /** The current mapping for a row: served model first, like the server. */
  function findOverride(m: PricedModel): UsagePriceModel | undefined {
    for (const id of [m.upstream_model, m.response_model ?? '', m.requested_model]) {
      const entries = id ? entriesFor(id) : [];
      if (entries.length) return entries[entries.length - 1];
    }
    return undefined;
  }

  /** Served models plus configured hardware models that have no price and no requests yet. */
  async function fetchModels(): Promise<PricedModel[]> {
    const [served, unpriced] = await Promise.all([pricedModels(range), unpricedLocalModels().catch(() => ({ models: [] }))]);
    const out = [...served.models];
    for (const u of unpriced.models) {
      if (!out.some((m) => m.provider === u.provider && m.upstream_model === u.model)) {
        out.unshift({
          provider: u.provider,
          requested_model: u.alias || u.model,
          upstream_model: u.model,
          class: 'local',
          requests: 0,
          total_tokens: 0,
          cost_usd: 0
        });
      }
    }
    return out;
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [s, m, c] = await Promise.all([
        pricingStatus(),
        fetchModels(),
        api.get<{ files: Credential[] }>('credentials'),
        config.ensure()
      ]);
      status = s;
      models = m;
      creds = c.files;
      fees = Object.fromEntries((pricing.subscriptions ?? []).map((s) => [s.account, String(s['monthly-usd'])]));
      classes = { ...(pricing['provider-classes'] ?? {}) };
      const target = focus.toLowerCase();
      const hit = target ? models.find((x) => [x.requested_model, x.upstream_model].some((v) => v.toLowerCase() === target)) : undefined;
      if (hit) startEdit(hit);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  async function refreshCatalog() {
    try {
      await refreshPricing();
      toasts.ok('Refreshing prices from models.dev');
      setTimeout(async () => {
        status = await pricingStatus().catch(() => status);
      }, 3000);
    } catch (e) {
      toasts.error(errorMessage(e));
    }
  }

  function startEdit(m: PricedModel) {
    const o = findOverride(m);
    const alias = m.requested_model && m.requested_model !== m.upstream_model;
    const num = (v: number | undefined) => (v === undefined ? '' : String(v));
    // Hardware prices default to the served model so they stay with it when the slug moves on.
    const served = m.upstream_model || m.requested_model;
    draft = {
      match: o?.model ?? (m.class === 'local' ? served : alias ? m.requested_model : served),
      mode: o && !o['price-as'] ? 'custom' : 'catalog',
      priceAs: o?.['price-as'] ?? (m.source === 'auto' ? (m.priced_as ?? '') : ''),
      input: num(o?.input),
      output: num(o?.output),
      cacheRead: num(o?.['cache-read']),
      cacheWrite: num(o?.['cache-write'])
    };
    editing = rowKey(m);
    editingModel = m;
    applyMode = '';
  }

  /** The current time as RFC3339 without milliseconds. */
  function nowRFC3339(): string {
    return new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
  }

  // Number inputs bind numbers (or null when cleared); prefilled values are strings.
  type Num = string | number | null | undefined;

  function parsePrice(v: Num): number | undefined {
    const t = String(v ?? '').trim();
    if (!t) return undefined;
    const n = Number(t);
    return Number.isFinite(n) && n >= 0 ? n : NaN;
  }

  async function saveModel() {
    const snap = config.snapshot();
    if (!snap) return;
    const match = draft.match.trim();
    let entry: UsagePriceModel;
    if (draft.mode === 'catalog') {
      if (!draft.priceAs.includes('/')) {
        toasts.error('Pick a model from the list.');
        return;
      }
      entry = { model: match, 'price-as': draft.priceAs };
    } else {
      const [input, output, cacheRead, cacheWrite] = [draft.input, draft.output, draft.cacheRead, draft.cacheWrite].map(parsePrice);
      if (input === undefined || output === undefined || [input, output, cacheRead, cacheWrite].some((n) => Number.isNaN(n))) {
        toasts.error('Enter input and output prices as non-negative numbers.');
        return;
      }
      entry = { model: match, input, output };
      if (cacheRead !== undefined) entry['cache-read'] = cacheRead;
      if (cacheWrite !== undefined) entry['cache-write'] = cacheWrite;
    }
    if (needsApplyChoice && !applyMode) {
      toasts.error('Choose whether the new price applies to past requests.');
      return;
    }
    const all = snap.doc.observability?.usage?.pricing?.models ?? [];
    let list: UsagePriceModel[];
    if (needsApplyChoice && applyMode === 'from-now') {
      // Keep the earlier prices for requests already made.
      entry.from = nowRFC3339();
      list = [...all, entry];
    } else {
      list = [...all.filter((o) => o.model.toLowerCase() !== match.toLowerCase()), entry];
    }
    if (await saveConfig(() => config.put('observability/usage/pricing/models', list, snap), `Price for ${match} saved`)) {
      editing = null;
      editingModel = null;
      await reloadModels();
    }
  }

  async function removeOverride(m: PricedModel) {
    const snap = config.snapshot();
    const o = findOverride(m);
    if (!snap || !o) return;
    const list = (snap.doc.observability?.usage?.pricing?.models ?? []).filter((x) => x.model.toLowerCase() !== o.model.toLowerCase());
    if (await saveConfig(() => config.put('observability/usage/pricing/models', list, snap), `Mapping for ${o.model} removed`)) {
      editing = null;
      await reloadModels();
    }
  }

  async function saveFees() {
    const snap = config.snapshot();
    if (!snap) return;
    const providerOf = (account: string) =>
      creds.find((c) => c.id === account)?.provider ?? (pricing.subscriptions ?? []).find((s) => s.account === account)?.provider;
    const list: { account: string; provider?: string; 'monthly-usd': number }[] = [];
    for (const [account, raw] of Object.entries(fees)) {
      const fee = parsePrice(raw);
      if (Number.isNaN(fee)) {
        toasts.error('Monthly fees must be non-negative numbers.');
        return;
      }
      if (fee) list.push({ account, provider: providerOf(account), 'monthly-usd': fee });
    }
    if (await saveConfig(() => config.put('observability/usage/pricing/subscriptions', list, snap), 'Subscription fees saved')) onsaved?.();
  }

  async function saveClasses() {
    const snap = config.snapshot();
    if (!snap) return;
    const map = Object.fromEntries(Object.entries(classes).filter(([, v]) => v));
    if (await saveConfig(() => config.put('observability/usage/pricing/provider-classes', map, snap), 'Cost classes saved')) {
      await reloadModels();
    }
  }

  async function reloadModels() {
    try {
      models = await fetchModels();
    } catch (e) {
      error = errorMessage(e);
    }
    onsaved?.();
  }
</script>

<Dialog bind:open title="Pricing" wide>
  <div class="stack">
    <p class="muted small intro">
      Costs are estimates at API list prices. <strong>Paid API</strong> traffic is real spend; <strong>Subscription</strong> and
      <strong>My hardware</strong> traffic show what the same requests would have cost on the API. Changes apply to all history.
    </p>
    {#if error}<Banner tone="bad">{error}</Banner>{/if}

    <div class="row wrap catalog">
      {#if status}
        <span class="small">
          Prices from <a href="https://models.dev" target="_blank" rel="noreferrer">models.dev</a>
          {#if status.models}· {int(status.models)} models · updated {relative(status.fetched_at)}{:else}· not loaded yet{/if}
        </span>
        {#if status.error}<span class="small bad">Last refresh failed: {status.error}</span>{/if}
      {/if}
      <div class="spacer"></div>
      <button class="btn sm" type="button" onclick={refreshCatalog}><Icon name="refresh" />Refresh prices</button>
    </div>

    <section aria-labelledby="pm-h">
      <h3 id="pm-h">Models in this period</h3>
      {#if unpricedLocal.length}
        <div class="gap-b">
          <Banner tone="warn"
            >{unpricedLocal.length === 1 ? 'A model' : `${unpricedLocal.length} models`} on your hardware {unpricedLocal.length === 1
              ? 'has'
              : 'have'} no price, so {unpricedLocal.length === 1 ? 'its' : 'their'} requests are not counted in Hardware value. Add a price whenever
            you swap the model behind a Spark slug.</Banner
          >
        </div>
      {/if}
      {#if loading && !models.length}
        <p class="muted">Loading…</p>
      {:else if !models.length}
        <p class="muted">No requests in this period.</p>
      {:else}
        <div class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>Model</th>
                <th>Counts as</th>
                <th>Priced as</th>
                <th class="r">Requests</th>
                <th class="r">Cost</th>
                <th><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {#each models as m (rowKey(m))}
                <tr>
                  <td class="model">
                    <code>{m.requested_model || m.upstream_model}</code>
                    {#if m.upstream_model && m.upstream_model !== m.requested_model}<span class="muted small upstream"
                        >→ {m.upstream_model}</span
                      >{/if}
                    <div class="muted small">
                      {providerName(m.provider)} · {m.requests ? `${compact(m.total_tokens)} tokens` : 'configured, no requests yet'}
                    </div>
                  </td>
                  <td class="nowrap">{CLASS_LABELS[m.class] ?? m.class}</td>
                  <td class="priced">
                    {#if m.priced_as}
                      <code class="small">{m.priced_as}</code>
                      <div class="muted small">
                        {m.source === 'override' ? 'mapped' : 'automatic'}{m.price
                          ? ` · ${perM(m.price.input)} / ${perM(m.price.output)} per 1M`
                          : ''}
                      </div>
                      {#if m.history?.length && m.from}
                        <div
                          class="muted small"
                          title={m.history
                            .map((v) => `${v.from ? shortDateTime(v.from) : 'Before'}: ${v.priced_as ?? 'unpriced'}`)
                            .join('\n')}
                        >
                          since {shortDateTime(m.from)} · {m.history.length - 1} earlier {m.history.length === 2 ? 'price' : 'prices'}
                        </div>
                      {/if}
                    {:else}
                      <span class="warn">Unpriced</span>
                    {/if}
                  </td>
                  <td class="r">{int(m.requests)}</td>
                  <td class="r">{m.priced_as ? usd(m.cost_usd) : '–'}</td>
                  <td class="r nowrap">
                    <button class="btn sm" type="button" onclick={() => (editing === rowKey(m) ? (editing = null) : startEdit(m))}
                      >{m.priced_as ? 'Change' : 'Set price'}</button
                    >
                  </td>
                </tr>
                {#if editing === rowKey(m)}
                  <tr class="editor">
                    <td colspan="6">
                      <div class="edit">
                        <div class="grid2">
                          <div class="field">
                            <label for="pm-match">Apply to</label>
                            <select id="pm-match" bind:value={draft.match}>
                              {#each [...new Set([m.requested_model, m.upstream_model, m.response_model ?? ''].filter(Boolean))] as id (id)}
                                <option value={id}>{id}{id === m.requested_model ? ' (requested)' : ' (served)'}</option>
                              {/each}
                            </select>
                          </div>
                          <div class="field">
                            <span class="label">Price source</span>
                            <div class="row">
                              <label class="radio"><input type="radio" bind:group={draft.mode} value="catalog" /> models.dev model</label>
                              <label class="radio"><input type="radio" bind:group={draft.mode} value="custom" /> Custom price</label>
                            </div>
                          </div>
                        </div>
                        {#if draft.mode === 'catalog'}
                          <ModelPicker bind:value={draft.priceAs} />
                          <span class="muted small"
                            >For your own hardware, pick what you would otherwise pay for, e.g. the same open model at a hosted provider.</span
                          >
                        {:else}
                          <div class="grid4">
                            <div class="field">
                              <label for="pm-in">Input $/1M</label><input
                                id="pm-in"
                                type="number"
                                min="0"
                                step="any"
                                bind:value={draft.input}
                              />
                            </div>
                            <div class="field">
                              <label for="pm-out">Output $/1M</label><input
                                id="pm-out"
                                type="number"
                                min="0"
                                step="any"
                                bind:value={draft.output}
                              />
                            </div>
                            <div class="field">
                              <label for="pm-cr">Cache read $/1M</label><input
                                id="pm-cr"
                                type="number"
                                min="0"
                                step="any"
                                placeholder="= input"
                                bind:value={draft.cacheRead}
                              />
                            </div>
                            <div class="field">
                              <label for="pm-cw">Cache write $/1M</label><input
                                id="pm-cw"
                                type="number"
                                min="0"
                                step="any"
                                placeholder="= input"
                                bind:value={draft.cacheWrite}
                              />
                            </div>
                          </div>
                        {/if}
                        {#if m.class === 'local'}
                          <span class="muted small"
                            >Hardware prices stay with the served model: when the slug moves to another model, the new one starts unpriced
                            and earlier requests keep this price.</span
                          >
                        {/if}
                        {#if needsApplyChoice}
                          <fieldset class="apply">
                            <legend>This model already has a price. Apply the new price to</legend>
                            <label class="radio"><input type="radio" bind:group={applyMode} value="all" /> All its past requests too</label>
                            <label class="radio"
                              ><input type="radio" bind:group={applyMode} value="from-now" /> Only requests from now on</label
                            >
                          </fieldset>
                        {/if}
                        <div class="row">
                          <button
                            class="btn primary sm"
                            type="button"
                            onclick={saveModel}
                            disabled={!config.loaded || (needsApplyChoice && !applyMode)}>Save</button
                          >
                          {#if findOverride(m)}<button class="btn sm" type="button" onclick={() => removeOverride(m)}
                              >Use automatic price</button
                            >{/if}
                          <button class="btn ghost sm" type="button" onclick={() => (editing = null)}>Cancel</button>
                        </div>
                      </div>
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section aria-labelledby="sub-h">
      <h3 id="sub-h">Subscription fees</h3>
      <p class="muted small">
        Monthly plan cost per signed-in account. Savings are the API value of the account's traffic minus the fee, prorated over the
        selected period.
      </p>
      {#if !subscriptionCreds.length && !orphanFees.length}
        <p class="muted">No signed-in accounts.</p>
      {:else}
        <div class="fees">
          {#each subscriptionCreds as c (c.id)}
            <label class="fee">
              <span class="truncate">{providerName(c.provider)} · {c.email || c.label || c.name}</span>
              <span class="money"
                >$<input
                  type="number"
                  min="0"
                  step="any"
                  placeholder="0"
                  bind:value={fees[c.id]}
                  aria-label="Monthly fee for {c.email || c.name}"
                />/mo</span
              >
            </label>
          {/each}
          {#each orphanFees as s (s.account)}
            <label class="fee">
              <span class="truncate muted">{s.account} (account removed)</span>
              <span class="money"
                >$<input type="number" min="0" step="any" bind:value={fees[s.account]} aria-label="Monthly fee for {s.account}" />/mo</span
              >
            </label>
          {/each}
        </div>
        <div class="row"><button class="btn sm" type="button" onclick={saveFees} disabled={!config.loaded}>Save fees</button></div>
      {/if}
    </section>

    {#if providers.length}
      <section aria-labelledby="cls-h">
        <h3 id="cls-h">Cost class by provider</h3>
        <p class="muted small">
          Signed-in accounts count as subscriptions and OpenAI-compatible servers on private or Tailscale addresses count as your hardware.
          Override that here.
        </p>
        <div class="fees">
          {#each providers as p (p)}
            <label class="fee">
              <span class="truncate">{providerName(p)}</span>
              <select bind:value={classes[p]}>
                <option value="">Automatic</option>
                <option value="api">Paid API</option>
                <option value="subscription">Subscription</option>
                <option value="local">My hardware</option>
              </select>
            </label>
          {/each}
        </div>
        <div class="row"><button class="btn sm" type="button" onclick={saveClasses} disabled={!config.loaded}>Save classes</button></div>
      </section>
    {/if}
  </div>
</Dialog>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .intro {
    margin: 0;
  }
  h3 {
    margin: 0 0 8px;
    font-size: 1rem;
  }
  section > p {
    margin: 0 0 10px;
  }
  .catalog {
    gap: 8px 12px;
    align-items: center;
  }
  .bad {
    color: var(--bad-text);
  }
  .warn {
    color: var(--warn-text);
  }
  /* overflow-wrap: anywhere drops the min-content width to one character,
     so a min-width keeps narrow screens from squeezing the column. */
  .model {
    min-width: 11rem;
    max-width: 260px;
    overflow-wrap: anywhere;
  }
  .model .upstream {
    display: block;
  }
  .priced {
    min-width: 10rem;
  }
  .nowrap {
    white-space: nowrap;
  }
  tr.editor td {
    background: var(--surface-2);
  }
  .edit {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 6px 0;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .grid4 {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 10px;
  }
  .gap-b {
    margin-bottom: 10px;
  }
  .apply {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 18px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 12px;
    margin: 0;
  }
  .apply legend {
    font-weight: 500;
    padding: 0 4px;
  }
  .radio {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-weight: 400;
  }
  .label {
    font-weight: 500;
  }
  .fees {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: 10px;
  }
  .fee {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-weight: 400;
  }
  .fee select {
    width: 180px;
  }
  .money {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex: none;
  }
  .money input {
    width: 100px;
  }
  @media (max-width: 640px) {
    .grid2,
    .grid4 {
      grid-template-columns: 1fr;
    }
  }
</style>
