<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError, errorMessage } from '../lib/api';
  import { config, saveConfig } from '../lib/config.svelte';
  import { bytes, dateTime, relative } from '../lib/format';
  import { downloadText } from '../lib/clipboard';
  import { router } from '../lib/router.svelte';
  import type { ErrorLogFile } from '../lib/types';
  import PageHeader from '../components/PageHeader.svelte';
  import Segmented from '../components/Segmented.svelte';
  import Toggle from '../components/Toggle.svelte';
  import Banner from '../components/Banner.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import Icon from '../components/Icon.svelte';

  type Tab = 'errors' | 'request' | 'app';
  const initial = router.query.get('tab');
  let tab = $state<Tab>(initial === 'request' || initial === 'app' ? initial : 'errors');

  let files = $state<ErrorLogFile[] | null>(null);
  let filesError = $state('');
  let selectedFile = $state('');
  let fileText = $state('');
  let fileLoading = $state(false);

  let lookupId = $state(router.query.get('id') ?? '');
  let lookupText = $state('');
  let lookupMsg = $state('');
  let lookupLoading = $state(false);

  let appLines = $state<string[] | null>(null);
  let appError = $state('');
  let follow = $state(false);
  let appFilter = $state('');
  let appBox: HTMLPreElement | undefined = $state();

  const logs = $derived(config.data?.observability?.logs ?? {});
  const requestLog = $derived(logs['request-log'] === true);
  const toFile = $derived(logs['logging-to-file'] === true);

  async function loadFiles() {
    try {
      const r = await api.get<{ files: ErrorLogFile[] }>('observability/logs/errors');
      files = r.files ?? [];
      filesError = '';
    } catch (e) {
      filesError = errorMessage(e);
    }
  }

  async function openFile(name: string) {
    selectedFile = name;
    fileLoading = true;
    fileText = '';
    try {
      const t = await api.get<string>(`observability/logs/errors/${encodeURIComponent(name)}`, { headers: { Accept: 'text/plain, */*' } });
      fileText = typeof t === 'string' ? t : JSON.stringify(t, null, 2);
    } catch (e) {
      fileText = errorMessage(e);
    } finally {
      fileLoading = false;
    }
  }

  async function lookup(e?: SubmitEvent) {
    e?.preventDefault();
    const id = lookupId.trim();
    if (!id) return;
    lookupLoading = true;
    lookupText = '';
    lookupMsg = '';
    try {
      const t = await api.get<string>(`observability/logs/requests/${encodeURIComponent(id)}`, { headers: { Accept: 'text/plain, */*' } });
      lookupText = typeof t === 'string' ? t : JSON.stringify(t, null, 2);
    } catch (err) {
      lookupMsg = err instanceof ApiError && err.status === 404 ? 'No log file found for that id.' : errorMessage(err);
    } finally {
      lookupLoading = false;
    }
  }

  async function loadApp() {
    try {
      const r = await api.get<{ lines: string[] }>('observability/logs', { query: { limit: 500 } });
      appLines = r.lines ?? [];
      appError = '';
      if (follow) requestAnimationFrame(() => appBox && (appBox.scrollTop = appBox.scrollHeight));
    } catch (e) {
      appError = errorMessage(e);
      appLines = null;
    }
  }

  onMount(() => {
    void config.ensure();
    void loadFiles();
    if (tab === 'request' && lookupId) void lookup();
    if (tab === 'app') void loadApp();
  });

  $effect(() => {
    if (!follow || tab !== 'app') return;
    const t = setInterval(loadApp, 4000);
    return () => clearInterval(t);
  });

  function setTab(t: Tab) {
    router.setQuery({ tab: t === 'errors' ? undefined : t });
    if (t === 'app' && appLines === null) void loadApp();
    if (t === 'errors') void loadFiles();
  }

  function setLog(key: 'request-log' | 'logging-to-file', value: boolean) {
    void saveConfig(
      () => config.patch({ observability: { logs: { [key]: value } } }),
      key === 'request-log' ? (value ? 'Request logging on' : 'Request logging off') : value ? 'Writing logs to files' : 'Logging to stdout'
    ).then(() => {
      if (key === 'logging-to-file') setTimeout(loadApp, 800);
      if (key === 'request-log') setTimeout(loadFiles, 800);
    });
  }

  // error-<endpoint with dashes>-<YYYY-MM-DDTHHMMSS>-<trace suffix>.log
  function parseName(name: string): { endpoint: string; id: string } {
    const m = /^error-(.+?)-\d{4}-\d{2}-\d{2}T\d{6}-([0-9a-zA-Z]+)\.log$/.exec(name);
    if (!m) return { endpoint: name, id: '' };
    return { endpoint: '/' + m[1].replace(/-/g, '/'), id: m[2] };
  }

  const filteredLines = $derived(
    appLines ? (appFilter.trim() ? appLines.filter((l) => l.toLowerCase().includes(appFilter.trim().toLowerCase())) : appLines) : []
  );
</script>

<PageHeader title="Logs" subtitle="Error logs for failed requests, full request logs, and the proxy's own log.">
  {#snippet actions()}
    <Segmented
      label="Log type"
      bind:value={tab}
      options={[
        { value: 'errors', label: 'Error logs' },
        { value: 'request', label: 'Find request' },
        { value: 'app', label: 'Proxy log' }
      ]}
      onchange={setTab}
    />
  {/snippet}
</PageHeader>

<section class="card card-pad gap settings">
  <Toggle
    checked={requestLog}
    label="Log every request"
    hint="Save full request and response bodies for every proxied request. Uses disk quickly; when off, only failed requests are saved."
    disabled={!config.loaded}
    onchange={(v) => setLog('request-log', v)}
  />
  <Toggle
    checked={toFile}
    label="Write the proxy log to files"
    hint="Needed to read the proxy log here; otherwise it goes to stdout (docker logs)."
    disabled={!config.loaded}
    onchange={(v) => setLog('logging-to-file', v)}
  />
  <p class="path">observability.logs.request-log · observability.logs.logging-to-file</p>
</section>

{#if tab === 'errors'}
  {#if requestLog}
    <Banner tone="info"
      >Request logging is on, so every request is saved in full; separate error logs are not written. Use <strong>Find request</strong> with a
      trace id from the Usage page.</Banner
    >
  {/if}
  {#if filesError}<Banner tone="bad">{filesError}</Banner>{/if}
  <div class="split">
    <section class="card list" aria-label="Error log files">
      {#if files === null}
        <p class="empty">Loading…</p>
      {:else if files.length === 0}
        <p class="empty">No error logs. Failed requests will appear here.</p>
      {:else}
        <ul>
          {#each files as f (f.name)}
            {@const p = parseName(f.name)}
            <li>
              <button type="button" class={['file', { active: selectedFile === f.name }]} onclick={() => openFile(f.name)}>
                <span class="truncate"><code>{p.endpoint}</code></span>
                <span class="small muted" title={dateTime(f.modified * 1000)}
                  >{relative(f.modified * 1000)} · {bytes(f.size)}{p.id ? ` · trace …${p.id}` : ''}</span
                >
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
    <section class="card card-pad viewer" aria-label="Log content">
      {#if !selectedFile}
        <p class="muted">Select a log file.</p>
      {:else}
        <div class="row head">
          <code class="truncate">{selectedFile}</code>
          <div class="spacer"></div>
          {#if fileText}<CopyButton text={fileText} label="Copy log" showLabel />{/if}
          {#if fileText}<button class="btn sm ghost" type="button" onclick={() => downloadText(selectedFile, fileText)}
              ><Icon name="download" size={14} />Download</button
            >{/if}
        </div>
        {#if fileLoading}<p class="muted">Loading…</p>{:else}<pre class="log">{fileText}</pre>{/if}
      {/if}
    </section>
  </div>
{:else if tab === 'request'}
  <section class="card card-pad stack">
    <form class="row wrap" onsubmit={lookup}>
      <label for="req-id" class="sr-only">Trace or request id</label>
      <input
        id="req-id"
        type="search"
        bind:value={lookupId}
        placeholder="Trace id (from Usage → request details)"
        style="max-width:480px"
      />
      <button class="btn primary" type="submit" disabled={lookupLoading || !lookupId.trim()}><Icon name="logs" />Find log</button>
    </form>
    {#if !requestLog}
      <p class="small muted">Request logging is off: only failed requests have logs.</p>
    {/if}
    {#if lookupLoading}<p class="muted">Looking…</p>{/if}
    {#if lookupMsg}<p class="muted">{lookupMsg}</p>{/if}
    {#if lookupText}
      <div class="row">
        <div class="spacer"></div>
        <CopyButton text={lookupText} label="Copy log" showLabel />
      </div>
      <pre class="log">{lookupText}</pre>
    {/if}
  </section>
{:else}
  <section class="card card-pad stack">
    {#if !toFile}
      <Banner tone="info">The proxy log goes to stdout. Turn on <strong>Write the proxy log to files</strong> above to read it here.</Banner
      >
    {:else}
      <div class="row wrap">
        <input type="search" bind:value={appFilter} placeholder="Filter lines" aria-label="Filter log lines" style="max-width:320px" />
        <label class="row small"><input type="checkbox" bind:checked={follow} />Auto-refresh</label>
        <div class="spacer"></div>
        <button class="btn sm" type="button" onclick={loadApp}><Icon name="refresh" size={14} />Refresh</button>
      </div>
      {#if appError}<Banner tone="bad">{appError}</Banner>{/if}
      {#if appLines !== null}
        <pre class="log app" bind:this={appBox}>{filteredLines.join('\n') || 'No lines.'}</pre>
        <p class="small muted">Showing the last {appLines.length} lines{appFilter ? `, ${filteredLines.length} matching` : ''}.</p>
      {/if}
    {/if}
  </section>
{/if}

<style>
  .gap {
    margin-bottom: 16px;
  }
  .settings {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .split {
    display: grid;
    grid-template-columns: minmax(240px, 340px) minmax(0, 1fr);
    gap: 16px;
    align-items: start;
    margin-top: 12px;
  }
  .list ul {
    list-style: none;
    margin: 0;
    padding: 4px;
    max-height: 70vh;
    overflow: auto;
  }
  .file {
    display: flex;
    flex-direction: column;
    width: 100%;
    text-align: left;
    padding: 8px 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    font: inherit;
    cursor: pointer;
    min-width: 0;
  }
  .file:hover {
    background: var(--surface-hover);
  }
  .file.active {
    background: var(--accent-soft);
  }
  .viewer {
    min-width: 0;
  }
  .head {
    margin-bottom: 8px;
  }
  pre.app {
    max-height: 65vh;
  }
  @media (max-width: 800px) {
    .split {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
