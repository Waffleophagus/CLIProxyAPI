<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import type { Attachment } from 'svelte/attachments';
  import { errorMessage } from '../lib/api';
  import { config, saveConfig } from '../lib/config.svelte';
  import { seriesColor } from '../lib/colors';
  import { compact, int, ms, pct, providerName, shortDateTime, dateTime, tps, usd } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import { appStatus } from '../lib/status.svelte';
  import {
    UsageDisabledError,
    resolveWindow,
    usageRequests,
    usageSummary,
    usageTimeseries,
    windowFor,
    type UsageFilters,
    type UsageRange,
    type UsageWindow
  } from '../lib/usage';
  import type { Aggregate, UsageGroup, UsageRow, UsageSummary, UsageTimeseries } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import Banner from '../components/Banner.svelte';
  import Icon from '../components/Icon.svelte';
  import StatTile from '../components/StatTile.svelte';
  import RequestDrawer from './usage/RequestDrawer.svelte';
  import PricingDialog from './usage/PricingDialog.svelte';

  type Range = UsageRange;
  type GroupBy = 'model' | 'upstream_model' | 'provider' | 'credential' | 'api_key' | 'day';
  type Metric = 'requests' | 'tokens' | 'cost' | 'speed';

  const GROUPS: { value: GroupBy; label: string }[] = [
    { value: 'model', label: 'Model' },
    { value: 'upstream_model', label: 'Upstream model' },
    { value: 'provider', label: 'Provider' },
    { value: 'credential', label: 'Account' },
    { value: 'api_key', label: 'Client key' },
    { value: 'day', label: 'Day' }
  ];

  const q = router.query;
  let range = $state<Range>((['24h', '7d', '30d', '90d', 'custom'].includes(q.get('range') ?? '') ? q.get('range') : '7d') as Range);
  let groupBy = $state<GroupBy>((GROUPS.some((g) => g.value === q.get('group')) ? q.get('group') : 'model') as GroupBy);
  let metric = $state<Metric>((['tokens', 'cost', 'speed'].includes(q.get('metric') ?? '') ? q.get('metric') : 'requests') as Metric);
  let fModel = $state(q.get('model') ?? '');
  let fProvider = $state(q.get('provider') ?? '');
  let fCredential = $state(q.get('credential') ?? '');
  let fKey = $state(q.get('api_key') ?? '');
  let fSuccess = $state(q.get('success') ?? '');
  let customFrom = $state(q.get('from') ?? '');
  let customTo = $state(q.get('to') ?? '');

  let summary = $state<UsageSummary | null>(null);
  let series = $state<UsageTimeseries | null>(null);
  let options = $state<Record<string, UsageGroup[]>>({});
  let rows = $state<UsageRow[]>([]);
  let nextBefore = $state<number | null>(null);
  let loadingMore = $state(false);
  let loading = $state(false);
  let disabled = $state(false);
  let error = $state('');
  let drawerOpen = $state(false);
  let selected = $state<UsageRow | null>(null);
  let pricingOpen = $state(false);
  let pricingFocus = $state('');
  let generation = 0;

  function toLocalInput(d: Date): string {
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  // The window is resolved on every load so relative ranges always end at the
  // time of the load; rows pages keep using the filters of the load they extend.
  let win = $state<UsageWindow>(untrack(() => resolveWindow(range, customFrom, customTo)));
  let active: UsageFilters = {};

  function currentFilters(w: UsageWindow): UsageFilters {
    return { from: w.from, to: w.to, model: fModel, provider: fProvider, credential: fCredential, api_key: fKey, success: fSuccess };
  }

  function syncURL() {
    router.setQuery({
      range,
      group: groupBy,
      metric: metric === 'requests' ? undefined : metric,
      model: fModel,
      provider: fProvider,
      credential: fCredential,
      api_key: fKey,
      success: fSuccess,
      from: range === 'custom' ? customFrom : undefined,
      to: range === 'custom' ? customTo : undefined
    });
  }

  async function loadAll() {
    const gen = ++generation;
    loading = true;
    error = '';
    syncURL();
    win = resolveWindow(range, customFrom, customTo);
    const f = currentFilters(win);
    active = f;
    const chartGroup = groupBy === 'day' ? '' : groupBy;
    try {
      const [s, ts, r] = await Promise.all([usageSummary(f, groupBy), usageTimeseries(f, win.bucket, chartGroup, 7), usageRequests(f, 50)]);
      if (gen !== generation) return;
      summary = s;
      series = ts;
      rows = r.requests;
      nextBefore = r.next_before;
      disabled = false;
    } catch (e) {
      if (gen !== generation) return;
      if (e instanceof UsageDisabledError) disabled = true;
      else error = errorMessage(e);
    } finally {
      if (gen === generation) loading = false;
    }
  }

  async function loadOptions() {
    const w = resolveWindow(range, customFrom, customTo);
    const f = { from: w.from, to: w.to };
    try {
      const [m, p, c, k] = await Promise.all([
        usageSummary(f, 'model', false),
        usageSummary(f, 'provider', false),
        usageSummary(f, 'credential', false),
        usageSummary(f, 'api_key', false)
      ]);
      options = { model: m.groups ?? [], provider: p.groups ?? [], credential: c.groups ?? [], api_key: k.groups ?? [] };
    } catch {
      // Filters still work by typing values in the URL; ignore.
    }
  }

  async function loadMore() {
    if (loadingMore || nextBefore === null) return;
    loadingMore = true;
    const gen = generation;
    try {
      const r = await usageRequests(active, 50, nextBefore);
      if (gen !== generation) return;
      rows = [...rows, ...r.requests];
      nextBefore = r.next_before;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loadingMore = false;
    }
  }

  // ?pricing=<model> (from the unpriced-model notice, also while this page is
  // already open) opens that model's price editor. Opening a dialog is a side
  // effect of navigation, so it runs in an effect; syncURL then drops the
  // parameter so reloads do not reopen it.
  $effect(() => {
    const focus = router.query.get('pricing');
    if (!focus) return;
    untrack(() => {
      openPricing(focus);
      syncURL();
    });
  });

  onMount(() => {
    void loadAll();
    void loadOptions();
  });

  // Loads the next page when the end of the table scrolls into view.
  const loadMoreOnView: Attachment<HTMLElement> = (node) => {
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) void loadMore();
      },
      { rootMargin: '300px' }
    );
    io.observe(node);
    return () => io.disconnect();
  };

  function changeRange(v: Range) {
    if (v === 'custom' && !customFrom) {
      const w = windowFor('7d');
      customFrom = toLocalInput(new Date(w.from));
      customTo = toLocalInput(new Date());
    }
    void loadAll();
    void loadOptions();
  }

  function applyGroupFilter(g: UsageGroup) {
    if (groupBy === 'model') fModel = g.key;
    else if (groupBy === 'provider') fProvider = g.key;
    else if (groupBy === 'credential') fCredential = g.key;
    else if (groupBy === 'api_key') fKey = g.key;
    else return;
    void loadAll();
  }

  function clearFilters() {
    fModel = fProvider = fCredential = fKey = fSuccess = '';
    void loadAll();
  }

  async function enableTracking() {
    const ok = await saveConfig(() => config.patch({ observability: { usage: { enabled: true } } }), 'Usage tracking turned on');
    if (ok) {
      appStatus.usageDisabled = false;
      setTimeout(loadAll, 1000);
    }
  }

  const hasFilters = $derived(Boolean(fModel || fProvider || fCredential || fKey || fSuccess));
  const groupLabel = $derived(GROUPS.find((g) => g.value === groupBy)?.label ?? '');
  const filterable = $derived(groupBy !== 'day' && groupBy !== 'upstream_model');

  const pricing = $derived(summary?.pricing);
  const chartMetric = $derived<Metric>(metric === 'cost' && !pricing ? 'requests' : metric);
  const metricName = $derived(
    chartMetric === 'tokens'
      ? 'Tokens'
      : chartMetric === 'cost'
        ? 'API-equivalent cost'
        : chartMetric === 'speed'
          ? 'Output speed'
          : 'Requests'
  );

  // Cost classes, in stacking order, for the ungrouped cost chart.
  const COST_CLASSES = [
    { key: 'api', label: 'Paid API', color: 'var(--series-1)', value: (p: Aggregate) => p.api_spend_usd },
    { key: 'subscription', label: 'Subscription value', color: 'var(--series-2)', value: (p: Aggregate) => p.subscription_value_usd },
    { key: 'local', label: 'Hardware value', color: 'var(--series-3)', value: (p: Aggregate) => p.local_value_usd }
  ];

  // Speed is a rate: a bucket without a qualifying stream has no value (a
  // gap in the line), not zero.
  function metricValue(p: Aggregate): number | null {
    if (chartMetric === 'speed') return p.output_tps > 0 ? p.output_tps : null;
    return chartMetric === 'tokens' ? p.total_tokens : chartMetric === 'cost' ? p.cost_usd : p.requests;
  }

  const rateAxis = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(1));

  const chartSeries = $derived.by(() => {
    const list = series?.series ?? [];
    if (chartMetric === 'cost' && list.length === 1 && list[0].key === 'total') {
      return COST_CLASSES.map((c) => ({ key: c.key, label: c.label, color: c.color, values: list[0].points.map(c.value) }));
    }
    return list.map((s) => ({
      key: s.key,
      label: s.key === 'total' ? metricName : s.key === '__other__' ? 'Other' : labelFor(s.label || s.key),
      color: s.key === 'total' ? 'var(--series-1)' : seriesColor(`${groupBy}:${s.key}`),
      values: s.points.map(metricValue)
    }));
  });

  const fees = $derived(pricing?.subscription_fees_usd ?? 0);
  const subscriptionSaved = $derived((summary?.totals.subscription_value_usd ?? 0) - fees);

  function costCell(a: Aggregate): string {
    if (a.requests > 0 && a.unpriced_requests >= a.requests) return 'Unpriced';
    return usd(a.cost_usd);
  }

  function openPricing(focus = '') {
    pricingFocus = focus;
    pricingOpen = true;
  }

  function labelFor(v: string): string {
    if (groupBy === 'provider') return providerName(v);
    if (groupBy === 'day') return new Date(v).toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
    return v || '(none)';
  }

  function optionLabel(kind: string, g: UsageGroup): string {
    if (kind === 'provider') return providerName(g.key);
    return g.label || g.key || '(none)';
  }

  function openRow(r: UsageRow) {
    selected = r;
    drawerOpen = true;
  }
</script>

<PageHeader
  title="Usage"
  subtitle="Every upstream attempt the proxy made, including retries. Times are in {Intl.DateTimeFormat().resolvedOptions().timeZone}."
>
  {#snippet actions()}
    {#if !disabled}<button class="btn" type="button" onclick={() => openPricing()}><Icon name="dollar" />Pricing</button>{/if}
    <button
      class="btn"
      type="button"
      onclick={() => {
        void loadAll();
        void loadOptions();
      }}
      disabled={loading}><Icon name="refresh" />Refresh</button
    >
  {/snippet}
</PageHeader>

{#if disabled}
  <div class="card card-pad off">
    <h2>Usage tracking is off</h2>
    <p class="muted">Turn it on to record tokens, latency and errors for every request in a local SQLite database.</p>
    <button class="btn primary" type="button" onclick={enableTracking} disabled={!config.loaded}>Turn on usage tracking</button>
    <p class="path">observability.usage.enabled</p>
  </div>
{:else}
  <div class="filters" role="search" aria-label="Usage filters">
    <Segmented
      label="Date range"
      bind:value={range}
      options={[
        { value: '24h', label: '24h' },
        { value: '7d', label: '7d' },
        { value: '30d', label: '30d' },
        { value: '90d', label: '90d' },
        { value: 'custom', label: 'Custom' }
      ]}
      onchange={changeRange}
    />
    {#if range === 'custom'}
      <label class="inline">From <input type="datetime-local" bind:value={customFrom} onchange={() => loadAll()} /></label>
      <label class="inline">To <input type="datetime-local" bind:value={customTo} onchange={() => loadAll()} /></label>
    {/if}
    <label class="inline">
      Group by
      <select bind:value={groupBy} onchange={() => loadAll()}>
        {#each GROUPS as g (g.value)}<option value={g.value}>{g.label}</option>{/each}
      </select>
    </label>
    <label class="inline">
      Model
      <select bind:value={fModel} onchange={() => loadAll()}>
        <option value="">All</option>
        {#if fModel && !(options.model ?? []).some((g) => g.key === fModel)}<option value={fModel}>{fModel}</option>{/if}
        {#each options.model ?? [] as g (g.key)}<option value={g.key}>{optionLabel('model', g)}</option>{/each}
      </select>
    </label>
    <label class="inline">
      Provider
      <select bind:value={fProvider} onchange={() => loadAll()}>
        <option value="">All</option>
        {#if fProvider && !(options.provider ?? []).some((g) => g.key === fProvider)}<option value={fProvider}>{fProvider}</option>{/if}
        {#each options.provider ?? [] as g (g.key)}<option value={g.key}>{optionLabel('provider', g)}</option>{/each}
      </select>
    </label>
    <label class="inline">
      Account
      <select bind:value={fCredential} onchange={() => loadAll()}>
        <option value="">All</option>
        {#if fCredential && !(options.credential ?? []).some((g) => g.key === fCredential)}<option value={fCredential}>{fCredential}</option
          >{/if}
        {#each options.credential ?? [] as g (g.key)}<option value={g.key}>{optionLabel('credential', g)}</option>{/each}
      </select>
    </label>
    <label class="inline">
      Client key
      <select bind:value={fKey} onchange={() => loadAll()}>
        <option value="">All</option>
        {#if fKey && !(options.api_key ?? []).some((g) => g.key === fKey)}<option value={fKey}>{fKey}</option>{/if}
        {#each options.api_key ?? [] as g (g.key)}<option value={g.key}>{optionLabel('api_key', g)}</option>{/each}
      </select>
    </label>
    <label class="inline">
      Result
      <select bind:value={fSuccess} onchange={() => loadAll()}>
        <option value="">All</option>
        <option value="true">Succeeded</option>
        <option value="false">Failed</option>
      </select>
    </label>
    {#if hasFilters}<button class="btn ghost sm" type="button" onclick={clearFilters}>Clear filters</button>{/if}
  </div>

  {#if error}<div class="gap"><Banner tone="bad">{error}</Banner></div>{/if}

  <div class={['totals', { stale: loading }]}>
    {#if summary}
      <div><span class="muted small">Requests</span><strong>{int(summary.totals.requests)}</strong></div>
      <div><span class="muted small">Success</span><strong>{pct(summary.totals.successes, summary.totals.requests)}</strong></div>
      <div><span class="muted small">Input tokens</span><strong>{compact(summary.totals.input_tokens)}</strong></div>
      <div><span class="muted small">Output tokens</span><strong>{compact(summary.totals.output_tokens)}</strong></div>
      <div>
        <span class="muted small">p50 / p95 latency</span><strong>{ms(summary.latency_p50_ms)} / {ms(summary.latency_p95_ms)}</strong>
      </div>
      <div title="Output tokens per second after the first token, over streamed requests">
        <span class="muted small">Output speed</span><strong>{tps(summary.totals.output_tps)}</strong>
      </div>
      {#if summary.store.dropped > 0}
        <div><span class="muted small">Dropped rows</span><strong class="warn">{int(summary.store.dropped)}</strong></div>
      {/if}
    {/if}
  </div>

  {#if summary && pricing}
    <div class={['costs', { stale: loading }]}>
      <StatTile
        label="API-equivalent cost"
        value={usd(summary.totals.cost_usd)}
        sub={summary.totals.unpriced_requests > 0
          ? `${int(summary.totals.unpriced_requests)} unpriced requests not included`
          : pricing.catalog.models
            ? 'At models.dev list prices'
            : 'Price catalog not loaded yet'}
        tone={summary.totals.unpriced_requests > 0 || !pricing.catalog.models ? 'warn' : ''}
      />
      <StatTile label="Paid API spend" value={usd(summary.totals.api_spend_usd)} sub="API keys and paid endpoints" />
      <StatTile
        label="Subscription value"
        value={usd(summary.totals.subscription_value_usd)}
        sub={fees > 0
          ? subscriptionSaved >= 0
            ? `${usd(fees)} in fees · saved ${usd(subscriptionSaved)}`
            : `${usd(fees)} in fees · ${usd(-subscriptionSaved)} more than API`
          : 'Set plan fees under Pricing to see savings'}
      />
      <StatTile
        label="Hardware value"
        value={usd(summary.totals.local_value_usd)}
        sub="What your own servers' requests would cost on the API"
      />
    </div>
    {#if summary.totals.unpriced_requests > 0}
      <p class="small muted unpriced">
        Some models have no known price. <button class="link" type="button" onclick={() => openPricing()}>Set prices</button>
      </p>
    {/if}
  {/if}

  <section class={['card card-pad gap', { stale: loading }]} aria-labelledby="chart-h">
    <div class="row wrap head">
      <h2 id="chart-h">
        {metricName} per {win.bucket}{groupBy !== 'day' ? ` by ${groupLabel.toLowerCase()}` : chartMetric === 'cost' ? ' by class' : ''}
      </h2>
      <div class="spacer"></div>
      <Segmented
        label="Metric"
        bind:value={metric}
        options={[
          { value: 'requests', label: 'Requests' },
          { value: 'tokens', label: 'Tokens' },
          { value: 'speed', label: 'Speed' },
          ...(pricing ? [{ value: 'cost' as const, label: 'Cost' }] : [])
        ]}
        onchange={syncURL}
      />
    </div>
    <ColumnChart
      title="{metricName} over time"
      buckets={series?.buckets ?? []}
      series={chartSeries}
      bucket={series?.bucket ?? win.bucket}
      height={240}
      format={chartMetric === 'tokens' ? compact : chartMetric === 'cost' ? usd : chartMetric === 'speed' ? tps : int}
      integer={chartMetric !== 'cost' && chartMetric !== 'speed'}
      axisFormat={chartMetric === 'cost' ? usd : chartMetric === 'speed' ? rateAxis : compact}
      lines={chartMetric === 'speed'}
      emptyText={chartMetric === 'speed' ? 'No streamed requests with a measurable speed in this period' : undefined}
    />
    {#if chartMetric === 'speed'}
      <p class="small muted note">
        Output tokens per second from the first token to the end of the response, over successful streamed requests. Recorded going forward;
        responses a server sends all at once are left out.
      </p>
    {/if}
  </section>

  <section class={['card gap', { stale: loading }]} aria-labelledby="breakdown-h">
    <div class="row head pad-h">
      <h2 id="breakdown-h">By {groupLabel.toLowerCase()}</h2>
      {#if filterable}<span class="muted small">Click a row to filter by it.</span>{/if}
    </div>
    {#if !summary?.groups?.length}
      <p class="empty">No requests match.</p>
    {:else}
      <div class="table-wrap">
        <table class="data">
          <thead>
            <tr>
              <th>{groupLabel}</th>
              <th class="r">Requests</th>
              <th class="r">Success</th>
              <th class="r">Input</th>
              <th class="r">Output</th>
              <th class="r">Total tokens</th>
              <th class="r">Avg latency</th>
              <th class="r">Avg first token</th>
              <th class="r">Speed</th>
              {#if pricing}<th class="r">Cost</th>{/if}
            </tr>
          </thead>
          <tbody>
            {#each summary.groups as g (g.key)}
              <tr
                class={{ clickable: filterable }}
                tabindex={filterable ? 0 : undefined}
                onclick={() => filterable && applyGroupFilter(g)}
                onkeydown={(e) => filterable && (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), applyGroupFilter(g))}
              >
                <td class="name">
                  {#if groupBy !== 'day'}<span class="swatch" style="background:{seriesColor(`${groupBy}:${g.key}`)}"></span>{/if}
                  <span class="truncate"
                    >{groupBy === 'day'
                      ? labelFor(g.key)
                      : groupBy === 'provider'
                        ? providerName(g.key)
                        : g.label || g.key || '(none)'}</span
                  >
                </td>
                <td class="r">{int(g.requests)}</td>
                <td class="r">{pct(g.successes, g.requests)}</td>
                <td class="r">{compact(g.input_tokens)}</td>
                <td class="r">{compact(g.output_tokens)}</td>
                <td class="r">{compact(g.total_tokens)}</td>
                <td class="r">{ms(g.avg_latency_ms)}</td>
                <td class="r">{ms(g.avg_ttft_ms)}</td>
                <td class="r nowrap">{tps(g.output_tps)}</td>
                {#if pricing}
                  <td class={['r', { warn: g.unpriced_requests > 0 && g.unpriced_requests >= g.requests }]}>{costCell(g)}</td>
                {/if}
              </tr>
            {/each}
          </tbody>
          <tfoot>
            <tr>
              <td>Total</td>
              <td class="r">{int(summary.totals.requests)}</td>
              <td class="r">{pct(summary.totals.successes, summary.totals.requests)}</td>
              <td class="r">{compact(summary.totals.input_tokens)}</td>
              <td class="r">{compact(summary.totals.output_tokens)}</td>
              <td class="r">{compact(summary.totals.total_tokens)}</td>
              <td class="r">{ms(summary.totals.avg_latency_ms)}</td>
              <td class="r">{ms(summary.totals.avg_ttft_ms)}</td>
              <td class="r nowrap">{tps(summary.totals.output_tps)}</td>
              {#if pricing}<td class="r">{usd(summary.totals.cost_usd)}</td>{/if}
            </tr>
          </tfoot>
        </table>
      </div>
    {/if}
  </section>

  <section class="card" aria-labelledby="req-h">
    <div class="row head pad-h">
      <h2 id="req-h">Requests</h2>
      <span class="muted small">Newest first. Click a row for details.</span>
    </div>
    {#if rows.length === 0}
      <p class="empty">{loading ? 'Loading…' : 'No requests match.'}</p>
    {:else}
      <div class="table-wrap">
        <table class="data">
          <thead>
            <tr>
              <th>Time</th>
              <th>Result</th>
              <th>Model</th>
              <th class="hide-md">Account</th>
              <th class="hide-md">Client key</th>
              <th class="r">Latency</th>
              <th class="r hide-md">Speed</th>
              <th class="r">Tokens in / out</th>
              {#if pricing}<th class="r">Cost</th>{/if}
            </tr>
          </thead>
          <tbody>
            {#each rows as r (r.id)}
              <tr
                class="clickable"
                tabindex="0"
                onclick={() => openRow(r)}
                onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), openRow(r))}
              >
                <td class="nowrap" title={dateTime(r.ts)}>{shortDateTime(r.ts)}</td>
                <td
                  >{#if r.success}<StatusBadge tone="good" text={String(r.status || 'OK')} />{:else}<StatusBadge
                      tone="bad"
                      text={String(r.status || 'ERR')}
                      title={r.error ?? ''}
                    />{/if}</td
                >
                <td class="model">
                  <code>{r.requested_model}</code>
                  {#if r.upstream_model && r.upstream_model !== r.requested_model}<span class="muted small">→ {r.upstream_model}</span>{/if}
                </td>
                <td class="hide-md truncate acct">{r.auth_label || providerName(r.provider)}</td>
                <td class="hide-md"><code>{r.client_key_masked || '–'}</code></td>
                <td class="r">{ms(r.latency_ms)}</td>
                <td class="r nowrap hide-md">{tps(r.output_tps)}</td>
                <td class="r nowrap">{compact(r.input_tokens)} / {compact(r.output_tokens)}</td>
                {#if pricing}<td class="r nowrap" title={r.priced_as ?? 'Unpriced'}>{r.cost_usd !== undefined ? usd(r.cost_usd) : '–'}</td
                  >{/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div {@attach loadMoreOnView} class="more">
        {#if nextBefore !== null}
          <button class="btn" type="button" onclick={loadMore} disabled={loadingMore}>{loadingMore ? 'Loading…' : 'Load more'}</button>
        {:else}
          <span class="muted small"
            >End of history{summary?.store.retention_days && summary.store.retention_days > 0
              ? ` (kept ${summary.store.retention_days} days)`
              : ''}.</span
          >
        {/if}
      </div>
    {/if}
  </section>
{/if}

<RequestDrawer bind:open={drawerOpen} row={selected} onsetprice={(model) => openPricing(model)} />
{#if pricingOpen}
  <PricingDialog bind:open={pricingOpen} range={win} focus={pricingFocus} onsaved={() => loadAll()} />
{/if}

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 10px 14px;
    align-items: center;
    margin-bottom: 16px;
  }
  .inline {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-weight: 500;
    font-size: 0.86rem;
    color: var(--text-2);
  }
  .inline select,
  .inline input {
    width: auto;
    max-width: 220px;
    min-height: 30px;
    padding-top: 3px;
    padding-bottom: 3px;
  }
  .gap {
    margin-bottom: 16px;
  }
  .off {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: flex-start;
  }
  .totals {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 28px;
    margin-bottom: 16px;
  }
  .totals div {
    display: flex;
    flex-direction: column;
  }
  .totals strong {
    font-size: 1.2rem;
    font-weight: 600;
  }
  .warn {
    color: var(--warn-text);
  }
  .costs {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 12px;
    margin-bottom: 8px;
  }
  .unpriced {
    margin: 0 0 16px;
  }
  .note {
    margin: 6px 0 0;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    font: inherit;
    text-decoration: underline;
  }
  .stale {
    opacity: 0.6;
    transition: opacity 0.2s;
  }
  .head {
    gap: 10px;
    margin-bottom: 8px;
  }
  .pad-h {
    padding: 14px 18px 2px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 8px;
    max-width: 360px;
  }
  .swatch {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    flex: none;
  }
  .model {
    max-width: 320px;
    overflow-wrap: anywhere;
  }
  .acct {
    max-width: 220px;
  }
  .nowrap {
    white-space: nowrap;
  }
  .more {
    display: flex;
    justify-content: center;
    padding: 14px;
  }
  table.data th:first-child,
  table.data td:first-child {
    padding-left: 18px;
  }
  table.data th:last-child,
  table.data td:last-child {
    padding-right: 18px;
  }
  @media (max-width: 900px) {
    .hide-md {
      display: none;
    }
  }
</style>
