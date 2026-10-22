<script lang="ts">
  import { api, ApiError, errorMessage } from '../../lib/api';
  import { config } from '../../lib/config.svelte';
  import { dateTime, int, ms, providerName, tps, usd } from '../../lib/format';
  import type { UsageRow } from '../../lib/types';
  import Dialog from '../../components/Dialog.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import CopyButton from '../../components/CopyButton.svelte';
  import Icon from '../../components/Icon.svelte';

  let {
    open = $bindable(false),
    row,
    onsetprice
  }: { open?: boolean; row: UsageRow | null; onsetprice?: (model: string) => void } = $props();

  const CLASS_LABELS: Record<string, string> = { api: 'Paid API', subscription: 'Subscription', local: 'My hardware' };

  type LogState = 'idle' | 'loading' | 'missing' | 'error' | 'ok';
  // The fetched log belongs to one row: these writable deriveds reset whenever
  // the drawer opens, closes or shows another row; loadLog overrides them.
  let log = $derived.by(() => {
    void [open, row];
    return '';
  });
  let logState = $derived.by<LogState>(() => {
    void [open, row];
    return 'idle';
  });
  let logError = $state('');

  async function loadLog() {
    if (!row) return;
    logState = 'loading';
    // Request and error log files are named after the trace id.
    const ids = [row.trace_id, row.request_id].filter((x): x is string => Boolean(x));
    for (const id of ids) {
      try {
        const text = await api.get<string>(`observability/logs/requests/${encodeURIComponent(id)}`, {
          headers: { Accept: 'text/plain, */*' }
        });
        log = typeof text === 'string' ? text : JSON.stringify(text, null, 2);
        logState = 'ok';
        return;
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) continue;
        logError = errorMessage(e);
        logState = 'error';
        return;
      }
    }
    logState = 'missing';
  }

  function clientKey(r: UsageRow): string {
    if (!r.client_key_masked) return '';
    const label = config.keyLabels[r.client_key_id];
    return `${label ? `${label} · ` : ''}${r.client_key_masked} (id ${r.client_key_id})`;
  }

  type Field = [string, string | number | boolean | undefined];
  const fields = $derived<Field[]>(
    row
      ? [
          ['Time', dateTime(row.ts)],
          ['Requested model', row.requested_model],
          ['Upstream model', row.upstream_model],
          ['Response model', row.response_model],
          ['Provider', providerName(row.provider)],
          ['Account', row.auth_label],
          ['Account type', row.auth_type],
          ['Account id', row.auth_id],
          ['Upstream URL', row.base_url],
          ['Client key', clientKey(row)],
          ['Endpoint', row.endpoint],
          ['Streaming', row.stream ? 'yes' : 'no'],
          ['HTTP status', row.status],
          ['Latency', ms(row.latency_ms)],
          ['Time to first token', ms(row.ttft_ms)],
          ['Output speed', row.output_tps ? tps(row.output_tps) : undefined],
          ['Input tokens', int(row.input_tokens)],
          ['Output tokens', int(row.output_tokens)],
          ['Reasoning tokens', int(row.reasoning_tokens)],
          ['Cache read tokens', int(row.cache_read_tokens)],
          ['Cache write tokens', int(row.cache_creation_tokens)],
          ['Total tokens', int(row.total_tokens)],
          ['API-equivalent cost', row.cost_usd !== undefined ? usd(row.cost_usd) : row.cost_class ? 'Unpriced' : undefined],
          ['Counts as', row.cost_class ? CLASS_LABELS[row.cost_class] : undefined],
          ['Priced as', row.priced_as],
          ['Reasoning effort', row.reasoning_effort],
          ['Service tier', row.service_tier],
          ['Session', row.session_id],
          ['Trace id', row.trace_id],
          ['Attempt id', row.request_id],
          ['Row id', row.id]
        ]
      : []
  );
</script>

<Dialog bind:open title="Request details" variant="drawer">
  {#if row}
    <div class="stack">
      <div class="row wrap">
        {#if row.success}<StatusBadge tone="good" text="Succeeded" />{:else}<StatusBadge
            tone="bad"
            text="Failed · {row.status || 'error'}"
          />{/if}
        <code>{row.requested_model}</code>
      </div>
      {#if row.error}
        <div class="err">
          <strong>Error</strong>
          <p>{row.error}</p>
        </div>
      {/if}
      {#if row.cost_class && row.cost_usd === undefined && onsetprice}
        <div class="row">
          <span class="muted small">This model has no known price.</span>
          <button
            class="btn sm"
            type="button"
            onclick={() => {
              open = false;
              onsetprice(row.upstream_model || row.requested_model);
            }}>Set price</button
          >
        </div>
      {/if}
      <dl>
        {#each fields as [k, v] (k)}
          {#if v !== undefined && v !== '' && v !== '–'}
            <dt>{k}</dt>
            <dd>{v}</dd>
          {/if}
        {/each}
      </dl>
      <div class="logbox">
        <div class="row">
          <h3>Request log</h3>
          <div class="spacer"></div>
          {#if logState === 'ok'}<CopyButton text={log} label="Copy log" showLabel />{/if}
        </div>
        {#if logState === 'idle'}
          <button class="btn" type="button" onclick={loadLog}><Icon name="logs" />Look for a saved log</button>
        {:else if logState === 'loading'}
          <p class="muted">Looking…</p>
        {:else if logState === 'missing'}
          <p class="muted small">
            No log file for this request. Every request is logged only while request logging is on; otherwise a log is kept only when the
            client got an error (a failed attempt that a retry recovered has none), and old error logs are rotated.
          </p>
        {:else if logState === 'error'}
          <p class="small bad">{logError}</p>
        {:else}
          <pre class="log">{log}</pre>
        {/if}
      </div>
    </div>
  {/if}
</Dialog>

<style>
  dl {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 6px 16px;
    margin: 0;
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
    font-variant-numeric: tabular-nums;
  }
  .err {
    background: var(--bad-soft);
    border-radius: var(--radius-sm);
    padding: 10px 12px;
    overflow-wrap: anywhere;
  }
  .logbox {
    border-top: 1px solid var(--border);
    padding-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .logbox .btn {
    align-self: flex-start;
  }
  .bad {
    color: var(--bad-text);
  }
</style>
