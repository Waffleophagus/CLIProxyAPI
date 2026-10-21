<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '../lib/api';
  import { config } from '../lib/config.svelte';
  import { compact, int, ms, pct, relative, shortDateTime, providerName, usd } from '../lib/format';
  import { link, router } from '../lib/router.svelte';
  import { poll } from '../lib/poll';
  import { credentialHealth, type HealthCategory } from '../lib/accounts';
  import { slugHealth } from '../lib/slugs';
  import { UsageDisabledError, usageRequests, usageSummary, usageTimeseries, windowFor } from '../lib/usage';
  import type { Credential, SlugStatus, UsageRow, UsageSummary, UsageTimeseries } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import StatTile from '../components/StatTile.svelte';
  import ColumnChart from '../components/ColumnChart.svelte';
  import Segmented from '../components/Segmented.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import Icon from '../components/Icon.svelte';
  import Banner from '../components/Banner.svelte';
  import { toasts } from '../lib/toast.svelte';

  type Range = '24h' | '7d' | '30d';
  let range = $state<Range>('24h');
  let summary = $state<UsageSummary | null>(null);
  let series = $state<UsageTimeseries | null>(null);
  let slugs = $state<SlugStatus[] | null>(null);
  let creds = $state<Credential[] | null>(null);
  let errors = $state<UsageRow[] | null>(null);
  let usageOff = $state(false);
  let loadError = $state('');
  let refreshing = $state(false);
  let rechecking = $state(false);

  async function loadUsage() {
    try {
      const w24 = windowFor('24h');
      const [s, e] = await Promise.all([usageSummary({ from: w24.from, to: w24.to }, 'model'), usageRequests({ success: 'false' }, 5)]);
      summary = s;
      errors = e.requests;
      usageOff = false;
      await loadSeries();
    } catch (e) {
      if (e instanceof UsageDisabledError) usageOff = true;
      else loadError = errorMessage(e);
    }
  }

  async function loadSeries() {
    const w = windowFor(range);
    try {
      series = await usageTimeseries({ from: w.from, to: w.to }, w.bucket);
    } catch (e) {
      if (!(e instanceof UsageDisabledError)) loadError = errorMessage(e);
    }
  }

  async function loadState() {
    const [s, c] = await Promise.allSettled([
      api.get<{ slugs: SlugStatus[] }>('routing/slugs'),
      api.get<{ files: Credential[] }>('credentials')
    ]);
    if (s.status === 'fulfilled') slugs = s.value.slugs ?? [];
    if (c.status === 'fulfilled') creds = c.value.files ?? [];
  }

  async function refresh() {
    refreshing = true;
    loadError = '';
    await Promise.all([loadUsage(), loadState()]);
    refreshing = false;
  }

  async function recheck() {
    rechecking = true;
    try {
      const r = await api.post<{ slugs: SlugStatus[] }>('routing/slugs/refresh');
      slugs = r.slugs ?? [];
      toasts.ok('Endpoints re-checked');
    } catch (e) {
      toasts.error(errorMessage(e));
    } finally {
      rechecking = false;
    }
  }

  onMount(() => {
    void refresh();
    return poll(refresh, 30_000);
  });

  const totals = $derived(summary?.totals);
  const failRate = $derived(totals && totals.requests ? totals.failures / totals.requests : 0);

  const total = $derived(series?.series[0]);
  const reqSeries = $derived(
    total
      ? [
          { key: 'ok', label: 'Succeeded', color: 'var(--series-1)', values: total.points.map((p) => p.successes) },
          { key: 'fail', label: 'Failed', color: 'var(--bad)', values: total.points.map((p) => p.failures) }
        ]
      : []
  );
  const tokSeries = $derived(
    total
      ? [
          { key: 'in', label: 'Input', color: 'var(--series-1)', values: total.points.map((p) => p.input_tokens) },
          { key: 'out', label: 'Output', color: 'var(--series-2)', values: total.points.map((p) => p.output_tokens) }
        ]
      : []
  );

  const health = $derived.by(() => {
    const counts: Record<HealthCategory, number> = { active: 0, cooling: 0, disabled: 0, relogin: 0, error: 0 };
    for (const c of creds ?? []) counts[credentialHealth(c).category]++;
    return counts;
  });
  const apiKeyCount = $derived.by(() => {
    const groups = config.data?.['api-keys'] ?? {};
    let n = 0;
    for (const p of ['codex', 'claude', 'xai', 'meta'] as const) {
      for (const g of groups[p] ?? []) n += g.keys?.length ?? 0;
    }
    return n;
  });
  const followSlugs = $derived((slugs ?? []).filter((s) => s.mode === 'follow'));
  const otherAliases = $derived((slugs ?? []).filter((s) => s.mode !== 'follow'));
</script>

<PageHeader title="Overview" subtitle="Health and activity of this proxy at a glance.">
  {#snippet actions()}
    <button class="btn" type="button" onclick={refresh} disabled={refreshing}><Icon name="refresh" />Refresh</button>
  {/snippet}
</PageHeader>

{#if loadError}
  <div style="margin-bottom:16px"><Banner tone="bad">{loadError}</Banner></div>
{/if}

<section aria-label="Last 24 hours" class={['kpis', { stale: refreshing && summary !== null }]}>
  {#if usageOff}
    <div class="card card-pad off">
      <p>
        Usage tracking is off. Turn it on in <a href={router.href('/settings')} use:link>Settings</a> to see requests, tokens and latency here.
      </p>
    </div>
  {:else}
    <StatTile
      label="Requests · 24h"
      value={totals ? int(totals.requests) : '…'}
      sub={totals ? `${int(totals.failures)} failed` : ''}
      tone={failRate > 0.1 ? 'bad' : ''}
    />
    <StatTile label="Success rate" value={totals ? pct(totals.successes, totals.requests) : '…'} sub="upstream attempts" />
    <StatTile
      label="Tokens in"
      value={totals ? compact(totals.input_tokens) : '…'}
      sub={totals?.cache_read_tokens ? `${compact(totals.cache_read_tokens)} cached` : 'incl. cache'}
    />
    <StatTile
      label="Tokens out"
      value={totals ? compact(totals.output_tokens) : '…'}
      sub={totals?.reasoning_tokens ? `${compact(totals.reasoning_tokens)} reasoning` : 'incl. reasoning'}
    />
    <StatTile label="Latency p50" value={summary ? ms(summary.latency_p50_ms) : '…'} sub="successful requests" />
    <StatTile
      label="Latency p95"
      value={summary ? ms(summary.latency_p95_ms) : '…'}
      sub={totals?.avg_ttft_ms ? `first token ~${ms(totals.avg_ttft_ms)}` : ''}
    />
    {#if summary?.pricing && totals}
      <StatTile
        label="API value · 24h"
        value={usd(totals.cost_usd)}
        sub={`${usd(totals.api_spend_usd)} paid · ${usd(totals.subscription_value_usd + totals.local_value_usd)} covered`}
      />
    {/if}
  {/if}
</section>

<div class="grid">
  <section class="card card-pad activity" aria-labelledby="activity-h">
    <div class="row wrap head">
      <h2 id="activity-h">Activity</h2>
      <div class="spacer"></div>
      <Segmented
        label="Time range"
        bind:value={range}
        options={[
          { value: '24h', label: '24 hours' },
          { value: '7d', label: '7 days' },
          { value: '30d', label: '30 days' }
        ]}
        onchange={loadSeries}
      />
    </div>
    {#if usageOff}
      <p class="muted">No usage data while tracking is off.</p>
    {:else}
      <h3 class="chart-title">Requests</h3>
      <ColumnChart
        title="Requests over time"
        buckets={series?.buckets ?? []}
        series={reqSeries}
        bucket={series?.bucket ?? 'hour'}
        height={170}
      />
      <h3 class="chart-title">Tokens</h3>
      <ColumnChart
        title="Tokens over time"
        buckets={series?.buckets ?? []}
        series={tokSeries}
        bucket={series?.bucket ?? 'hour'}
        height={150}
        format={compact}
      />
    {/if}
  </section>

  <div class="side">
    <section class="card card-pad" aria-labelledby="slugs-h">
      <div class="row head">
        <h2 id="slugs-h">Slugs</h2>
        <div class="spacer"></div>
        <button
          class="btn ghost sm"
          type="button"
          onclick={recheck}
          disabled={rechecking || followSlugs.length === 0}
          title="Re-check follow-mode endpoints now"
        >
          <Icon name="refresh" size={14} />{rechecking ? 'Checking…' : 'Re-check'}
        </button>
      </div>
      {#if slugs === null}
        <p class="muted">Loading…</p>
      {:else if slugs.length === 0}
        <p class="muted small">
          No slugs yet. <a href={router.href('/slugs')} use:link>Create one</a> to give a local model a stable name.
        </p>
      {:else}
        <ul class="slugs">
          {#each followSlugs as s (s.group + s.alias)}
            {@const h = slugHealth(s)}
            <li>
              <div class="row">
                <strong class="alias">{s.alias}</strong>
                <div class="spacer"></div>
                <StatusBadge tone={h.tone} text={h.text} title={s.last_error ?? ''} />
              </div>
              <div class="resolved">
                <Icon name="chevronRight" size={14} />
                {#if s.resolved_model}<code>{s.resolved_model}</code>{:else}<span class="muted">no model yet</span>{/if}
              </div>
              <div class="small muted">{s.group} · checked {relative(s.last_check_at)}</div>
            </li>
          {/each}
        </ul>
        {#if otherAliases.length}
          <p class="small muted aliases">
            Pinned aliases: {otherAliases.map((a) => a.alias).join(', ')}
          </p>
        {/if}
        <a class="small" href={router.href('/slugs')} use:link>Manage slugs →</a>
      {/if}
    </section>

    <section class="card card-pad" aria-labelledby="acct-h">
      <div class="row head">
        <h2 id="acct-h">Accounts</h2>
        <div class="spacer"></div>
        <a class="small" href={router.href('/accounts')} use:link>Manage →</a>
      </div>
      {#if creds === null}
        <p class="muted">Loading…</p>
      {:else if creds.length === 0}
        <p class="muted small">
          No signed-in accounts yet. <a href={router.href('/accounts')} use:link>Add a Claude, Codex or other account</a> to route requests through
          a subscription.
        </p>
        <p class="small muted">{apiKeyCount} provider API key{apiKeyCount === 1 ? '' : 's'} configured.</p>
      {:else}
        <ul class="health">
          <li><StatusBadge tone="good" text="Active" /><span class="num">{health.active}</span></li>
          <li><StatusBadge tone="warn" text="Cooling down" /><span class="num">{health.cooling}</span></li>
          <li><StatusBadge tone="bad" text="Needs sign-in" /><span class="num">{health.relogin}</span></li>
          <li><StatusBadge tone="bad" text="Error" /><span class="num">{health.error}</span></li>
          <li><StatusBadge tone="neutral" text="Disabled" /><span class="num">{health.disabled}</span></li>
        </ul>
        <p class="small muted">
          {creds.length} signed-in account{creds.length === 1 ? '' : 's'} · {apiKeyCount} provider API key{apiKeyCount === 1 ? '' : 's'}
        </p>
      {/if}
    </section>
  </div>
</div>

<div class="grid bottom">
  <section class="card" aria-labelledby="models-h">
    <div class="row head card-pad-h">
      <h2 id="models-h">Top models · 24h</h2>
      <div class="spacer"></div>
      <a class="small" href={router.href('/usage', { group: 'model' })} use:link>Explore usage →</a>
    </div>
    {#if !summary?.groups?.length}
      <p class="empty">{usageOff ? 'Usage tracking is off.' : 'No requests in the last 24 hours.'}</p>
    {:else}
      <div class="table-wrap">
        <table class="data">
          <thead>
            <tr
              ><th>Model</th><th class="r">Requests</th><th class="r">Success</th><th class="r">Tokens</th><th class="r">Avg latency</th
              ></tr
            >
          </thead>
          <tbody>
            {#each summary.groups.slice(0, 8) as g (g.key)}
              <tr>
                <td class="truncate" style="max-width:260px"><code>{g.label || g.key || '(none)'}</code></td>
                <td class="r">{int(g.requests)}</td>
                <td class="r">{pct(g.successes, g.requests)}</td>
                <td class="r">{compact(g.total_tokens)}</td>
                <td class="r">{ms(g.avg_latency_ms)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="card" aria-labelledby="errs-h">
    <div class="row head card-pad-h">
      <h2 id="errs-h">Recent errors</h2>
      <div class="spacer"></div>
      <a class="small" href={router.href('/usage', { success: 'false' })} use:link>All failures →</a>
    </div>
    {#if !errors?.length}
      <p class="empty">{usageOff ? 'Usage tracking is off.' : 'No failed requests recorded.'}</p>
    {:else}
      <ul class="errors">
        {#each errors as r (r.id)}
          <li>
            <div class="row small">
              <span class="status-code">{r.status || 'ERR'}</span>
              <code class="truncate">{r.requested_model}</code>
              <span class="muted truncate">{providerName(r.provider)}</span>
              <div class="spacer"></div>
              <span class="muted nowrap" title={r.ts}>{shortDateTime(r.ts)}</span>
            </div>
            {#if r.error}<p class="small err-text">{r.error}</p>{/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .kpis {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 12px;
    margin-bottom: 16px;
    transition: opacity 0.2s;
  }
  .stale {
    opacity: 0.7;
  }
  .off {
    grid-column: 1 / -1;
  }
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(280px, 1fr);
    gap: 16px;
    margin-bottom: 16px;
    align-items: start;
  }
  .grid.bottom {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  }
  .side {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .head {
    margin-bottom: 10px;
    gap: 8px;
  }
  .card-pad-h {
    padding: 14px 18px 0;
  }
  .chart-title {
    font-size: 0.86rem;
    color: var(--text-muted);
    font-weight: 500;
    margin: 10px 0 6px;
  }
  .slugs,
  .health,
  .errors {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .slugs li {
    padding: 10px 0;
    border-bottom: 1px solid var(--border);
  }
  .slugs li:first-child {
    padding-top: 0;
  }
  .alias {
    font-size: 1.05rem;
  }
  .resolved {
    display: flex;
    align-items: center;
    gap: 4px;
    margin: 2px 0;
    color: var(--text-2);
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .aliases {
    margin: 8px 0;
  }
  .health li {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 4px 0;
  }
  .health .num {
    font-weight: 600;
  }
  .health + p {
    margin-top: 8px;
  }
  .errors li {
    padding: 10px 18px;
    border-top: 1px solid var(--border);
  }
  .errors {
    margin-top: 10px;
  }
  .errors code {
    flex: none;
    max-width: 45%;
  }
  .status-code {
    font-weight: 600;
    color: var(--bad-text);
    font-variant-numeric: tabular-nums;
  }
  .err-text {
    color: var(--text-2);
    margin-top: 2px;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .nowrap {
    white-space: nowrap;
  }
  table.data th:first-child,
  table.data td:first-child {
    padding-left: 18px;
  }
  table.data th:last-child,
  table.data td:last-child {
    padding-right: 18px;
  }
  @media (max-width: 1000px) {
    .grid,
    .grid.bottom {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
