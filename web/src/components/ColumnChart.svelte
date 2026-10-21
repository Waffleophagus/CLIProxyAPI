<script lang="ts" module>
  export type ChartSeries = { key: string; label: string; color: string; values: number[] };
</script>

<script lang="ts">
  // Stacked column chart over time buckets, hand-rolled SVG.
  // Marks follow the dataviz spec: <=24px columns, 4px rounded data-end,
  // 2px surface gap between stacked segments, hairline grid, one y-axis,
  // per-column hover/focus tooltip listing every series, and a table view.
  import { compact, int } from '../lib/format';

  let {
    buckets,
    series,
    bucket = 'hour',
    height = 200,
    format = int,
    title,
    emptyText = 'No data in this period'
  }: {
    buckets: string[];
    series: ChartSeries[];
    bucket?: 'hour' | 'day';
    height?: number;
    format?: (n: number) => string;
    title: string;
    emptyText?: string;
  } = $props();

  const M = { top: 10, right: 6, bottom: 26, left: 44 };
  let width = $state(600);
  let active = $state<number | null>(null);
  let showTable = $state(false);

  const n = $derived(buckets.length);
  const totals = $derived(buckets.map((_, i) => series.reduce((s, se) => s + (se.values[i] ?? 0), 0)));
  const maxTotal = $derived(Math.max(0, ...totals));
  const plotW = $derived(Math.max(10, width - M.left - M.right));
  const plotH = $derived(Math.max(10, height - M.top - M.bottom));

  // Four gridline steps of a "nice" size (1, 2, 2.5, 5 x 10^k), never below 1
  // so integer counts get integer ticks.
  function niceStep(v: number): number {
    const raw = Math.max(1, v / 4);
    const exp = Math.pow(10, Math.floor(Math.log10(raw)));
    for (const m of [1, 2, 2.5, 5, 10]) {
      if (m * exp >= raw) return m * exp;
    }
    return 10 * exp;
  }
  const step = $derived(niceStep(maxTotal));
  const yMax = $derived(step * 4);
  const ticks = $derived([0, 1, 2, 3, 4].map((i) => i * step));
  const slot = $derived(n ? plotW / n : plotW);
  const barW = $derived(Math.max(1, Math.min(24, slot * 0.72, slot - 1)));
  const y = (v: number) => M.top + plotH - (v / yMax) * plotH;

  type Seg = { x: number; y: number; w: number; h: number; color: string; top: boolean };
  const columns = $derived.by(() => {
    const out: Seg[][] = [];
    for (let i = 0; i < n; i++) {
      const segs: Seg[] = [];
      let acc = 0;
      const nonZero = series.map((s, si) => ({ s, si, v: s.values[i] ?? 0 })).filter((x) => x.v > 0);
      nonZero.forEach((item, idx) => {
        const y0 = y(acc);
        acc += item.v;
        const y1 = y(acc);
        // Leave a 2px surface gap below every segment except the bottom one.
        const gap = idx > 0 ? 2 : 0;
        const h = Math.max(0, y0 - y1 - gap);
        if (h <= 0.25) return;
        segs.push({
          x: M.left + i * slot + (slot - barW) / 2,
          y: y1,
          w: barW,
          h,
          color: item.s.color,
          top: idx === nonZero.length - 1
        });
      });
      out.push(segs);
    }
    return out;
  });

  function segPath(s: Seg): string {
    // Rounded data-end (top), square at the baseline.
    const r = Math.min(4, s.w / 2, s.h);
    if (!s.top || r < 1) return `M${s.x},${s.y}h${s.w}v${s.h}h${-s.w}z`;
    return `M${s.x},${s.y + s.h}V${s.y + r}Q${s.x},${s.y} ${s.x + r},${s.y}H${s.x + s.w - r}Q${s.x + s.w},${s.y} ${s.x + s.w},${s.y + r}V${s.y + s.h}z`;
  }

  const hourFmt = new Intl.DateTimeFormat(undefined, { hour: 'numeric' });
  const dayFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
  const fullHourFmt = new Intl.DateTimeFormat(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit'
  });
  const fullDayFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' });

  function tickLabel(iso: string): string {
    const d = new Date(iso);
    if (bucket === 'day') return dayFmt.format(d);
    if (d.getHours() === 0) return dayFmt.format(d);
    return hourFmt.format(d);
  }
  function fullLabel(iso: string): string {
    const d = new Date(iso);
    return bucket === 'day' ? fullDayFmt.format(d) : fullHourFmt.format(d);
  }

  const xTicks = $derived.by(() => {
    if (!n) return [] as number[];
    const every = Math.max(1, Math.ceil(64 / slot));
    const out: number[] = [];
    for (let i = 0; i < n; i += every) out.push(i);
    return out;
  });

  function onKey(e: KeyboardEvent) {
    if (!n) return;
    if (e.key === 'ArrowRight') active = active === null ? 0 : Math.min(n - 1, active + 1);
    else if (e.key === 'ArrowLeft') active = active === null ? n - 1 : Math.max(0, active - 1);
    else if (e.key === 'Home') active = 0;
    else if (e.key === 'End') active = n - 1;
    else if (e.key === 'Escape') active = null;
    else return;
    e.preventDefault();
  }

  const tipLeft = $derived.by(() => {
    if (active === null) return 0;
    const cx = M.left + active * slot + slot / 2;
    const tipW = 220;
    return cx + 12 + tipW > width ? Math.max(0, cx - 12 - tipW) : cx + 12;
  });
</script>

<div class="chart">
  {#if series.length > 1}
    <ul class="legend" aria-label="Legend">
      {#each series as s (s.key)}
        <li><span class="swatch" style="background:{s.color}"></span>{s.label}</li>
      {/each}
    </ul>
  {/if}

  {#if !n || maxTotal === 0}
    <div class="empty-chart" style="height:{height}px">{emptyText}</div>
  {:else}
    <!-- The plot is a keyboard-explorable widget: arrow keys move the readout. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
    <div
      class="plot"
      bind:clientWidth={width}
      style="height:{height}px"
      role="group"
      tabindex="0"
      aria-label="{title}. Use the arrow keys to read values."
      onkeydown={onKey}
      onpointerleave={() => (active = null)}
      onblur={() => (active = null)}
    >
      <svg {width} {height} aria-hidden="true">
        {#each ticks as t, i (i)}
          <line x1={M.left} x2={width - M.right} y1={y(t)} y2={y(t)} class={i === 0 ? 'baseline' : 'grid'} />
          <text x={M.left - 8} y={y(t)} dy="0.32em" text-anchor="end" class="axis">{compact(t)}</text>
        {/each}
        {#if active !== null}
          <rect x={M.left + active * slot} y={M.top} width={slot} height={plotH} class="hoverband" />
        {/if}
        {#each columns as segs, i (i)}
          <g class={{ dim: active !== null && active !== i }}>
            {#each segs as s, j (j)}
              <path d={segPath(s)} fill={s.color} />
            {/each}
          </g>
        {/each}
        {#each xTicks as i (i)}
          <text x={M.left + i * slot + slot / 2} y={height - 8} text-anchor="middle" class="axis">{tickLabel(buckets[i])}</text>
        {/each}
        {#each buckets as _, i (i)}
          <rect
            x={M.left + i * slot}
            y={M.top}
            width={slot}
            height={plotH}
            fill="transparent"
            role="presentation"
            onpointerenter={() => (active = i)}
            onpointermove={() => (active = i)}
          />
        {/each}
      </svg>
      {#if active !== null}
        <div class="tooltip" style="left:{tipLeft}px" role="status">
          <div class="tt-title">{fullLabel(buckets[active])}</div>
          {#each series as s (s.key)}
            <div class="tt-row">
              <span class="key" style="background:{s.color}"></span>
              <strong class="num">{format(s.values[active] ?? 0)}</strong>
              <span class="tt-label">{s.label}</span>
            </div>
          {/each}
          {#if series.length > 1}
            <div class="tt-row total">
              <span class="key blank"></span><strong class="num">{format(totals[active])}</strong><span class="tt-label">Total</span>
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  <div class="chart-foot">
    <button type="button" class="btn ghost sm" onclick={() => (showTable = !showTable)} aria-expanded={showTable}>
      {showTable ? 'Hide table' : 'Show as table'}
    </button>
  </div>
  {#if showTable}
    <div class="table-wrap tbl">
      <table class="data">
        <caption class="sr-only">{title}</caption>
        <thead>
          <tr>
            <th>{bucket === 'day' ? 'Day' : 'Hour'}</th>
            {#each series as s (s.key)}<th class="r">{s.label}</th>{/each}
            {#if series.length > 1}<th class="r">Total</th>{/if}
          </tr>
        </thead>
        <tbody>
          {#each buckets as b, i (b)}
            {#if totals[i] > 0}
              <tr>
                <td>{fullLabel(b)}</td>
                {#each series as s (s.key)}<td class="r">{format(s.values[i] ?? 0)}</td>{/each}
                {#if series.length > 1}<td class="r">{format(totals[i])}</td>{/if}
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .chart {
    min-width: 0;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    list-style: none;
    margin: 0 0 8px;
    padding: 0;
    font-size: 0.84rem;
    color: var(--text-2);
  }
  .legend li {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .swatch {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    flex: none;
  }
  .plot {
    position: relative;
    width: 100%;
    outline-offset: 4px;
  }
  svg {
    display: block;
    overflow: visible;
  }
  .grid {
    stroke: var(--grid);
    stroke-width: 1;
    shape-rendering: crispEdges;
  }
  .baseline {
    stroke: var(--baseline);
    stroke-width: 1;
    shape-rendering: crispEdges;
  }
  .axis {
    fill: var(--axis);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }
  .hoverband {
    fill: var(--surface-hover);
  }
  g.dim {
    opacity: 0.55;
  }
  .empty-chart {
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius-sm);
  }
  .tooltip {
    position: absolute;
    top: 0;
    width: 220px;
    pointer-events: none;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    box-shadow: var(--shadow-pop);
    padding: 8px 10px;
    font-size: 0.84rem;
    z-index: 5;
  }
  .tt-title {
    color: var(--text-muted);
    margin-bottom: 4px;
  }
  .tt-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .tt-row.total {
    border-top: 1px solid var(--border);
    margin-top: 4px;
    padding-top: 4px;
  }
  .key {
    width: 12px;
    height: 2px;
    border-radius: 1px;
    flex: none;
  }
  .key.blank {
    background: transparent;
  }
  .tt-label {
    color: var(--text-2);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chart-foot {
    display: flex;
    justify-content: flex-end;
    margin-top: 2px;
  }
  .tbl {
    max-height: 280px;
    overflow: auto;
    margin-top: 4px;
  }
</style>
