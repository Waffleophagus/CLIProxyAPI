<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { auth } from '../lib/auth.svelte';
  import { config, saveConfig } from '../lib/config.svelte';
  import { appStatus } from '../lib/status.svelte';
  import { confirm } from '../lib/confirm.svelte';
  import { dirtyFields, rebaseDraft, revertFields } from '../lib/drafts';
  import { router } from '../lib/router.svelte';
  import type { ConfigDoc } from '../lib/types';
  import Toggle from '../components/Toggle.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import Banner from '../components/Banner.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import PageHeader from '../components/PageHeader.svelte';
  import Icon from '../components/Icon.svelte';
  import RawConfig from './settings/RawConfig.svelte';

  type Draft = {
    strategy: string;
    affinity: boolean;
    retry: number;
    retryWait: number;
    disableCooling: boolean;
    usageEnabled: boolean;
    retention: number;
    debug: boolean;
    allowRemote: boolean;
    disableUI: boolean;
    tsEnabled: boolean;
    tsHostname: string;
    tsHTTPS: boolean;
    tsUsers: string;
  };

  function fromConfig(c: ConfigDoc | null): Draft {
    const ts = c?.tailscale ?? {};
    return {
      strategy: c?.routing?.strategy ?? 'round-robin',
      affinity: c?.routing?.['session-affinity'] === true,
      retry: c?.routing?.retry?.['request-retry'] ?? 0,
      retryWait: c?.routing?.retry?.['max-retry-interval'] ?? 0,
      disableCooling: c?.routing?.cooldown?.['disable-cooling'] === true,
      usageEnabled: c?.observability?.usage?.enabled !== false,
      retention: c?.observability?.usage?.['retention-days'] ?? 90,
      debug: c?.observability?.logs?.debug === true,
      allowRemote: c?.management?.['allow-remote'] === true,
      disableUI: c?.management?.['disable-ui'] === true,
      tsEnabled: ts.enabled === true,
      tsHostname: ts.hostname ?? '',
      tsHTTPS: ts.https !== false,
      tsUsers: (ts['management-users'] ?? []).join('\n')
    };
  }

  // One form per card. Each card saves only its own fields; a reload after any
  // save (or a change elsewhere) refreshes clean fields from the server and
  // keeps unsaved edits in the other cards.
  const CARDS = {
    routing: ['strategy', 'affinity', 'retry', 'retryWait', 'disableCooling'],
    usage: ['usageEnabled', 'retention'],
    mgmt: ['allowRemote', 'disableUI', 'debug'],
    ts: ['tsEnabled', 'tsHostname', 'tsHTTPS', 'tsUsers']
  } as const satisfies Record<string, readonly (keyof Draft)[]>;
  type Card = keyof typeof CARDS;

  let draft = $state<Draft>(fromConfig(null));
  let base = $state<Draft>(fromConfig(null));
  let saving = $state<Card | null>(null);

  function syncFromServer(reset: readonly (keyof Draft)[] = []) {
    const server = fromConfig(config.data);
    draft = rebaseDraft(draft, base, server, reset);
    base = server;
  }

  $effect(() => {
    void config.data;
    untrack(() => syncFromServer());
  });

  onMount(() => {
    void config.ensure();
    void appStatus.refreshTailscale();
    return router.guard(() => (dirtyFields(draft, base).length ? 'You have unsaved settings. Leave this page and discard them?' : null));
  });

  const ts = $derived(auth.tailscale);
  const loaded = $derived(config.data !== null && config.etag !== null);
  const dirty = (card: Card) => dirtyFields(draft, base, CARDS[card]).length > 0;
  const routingDirty = $derived(dirty('routing'));
  const usageDirty = $derived(dirty('usage'));
  const mgmtDirty = $derived(dirty('mgmt'));
  const tsDirty = $derived(dirty('ts'));

  async function save(card: Card, patch: ConfigDoc, ok: string): Promise<boolean> {
    if (!loaded) return false;
    saving = card;
    const done = await saveConfig(() => config.patch(patch), ok);
    saving = null;
    // The saved card now mirrors the server (including any normalization);
    // the other cards keep their edits.
    if (done) syncFromServer(CARDS[card]);
    return done;
  }

  function saveRouting() {
    void save(
      'routing',
      {
        routing: {
          strategy: draft.strategy,
          'session-affinity': draft.affinity,
          retry: { 'request-retry': Number(draft.retry) || 0, 'max-retry-interval': Number(draft.retryWait) || 0 },
          cooldown: { 'disable-cooling': draft.disableCooling }
        }
      },
      'Routing saved'
    );
  }

  async function saveUsage() {
    const enabled = draft.usageEnabled;
    if (
      (await save(
        'usage',
        { observability: { usage: { enabled, 'retention-days': Number(draft.retention) || 90 } } },
        'Usage settings saved'
      )) &&
      enabled
    ) {
      appStatus.usageDisabled = false;
    }
  }

  async function saveMgmt() {
    if (draft.disableUI && !base.disableUI) {
      const ok = await confirm({
        title: 'Turn off this web UI?',
        message:
          'After saving, /ui/ returns 404 and this page stops working. Re-enable it by setting management.disable-ui: false in config.yaml.',
        confirmLabel: 'Turn off UI',
        danger: true
      });
      if (!ok) return;
    }
    if (!draft.allowRemote && base.allowRemote && !auth.identity) {
      const ok = await confirm({
        title: 'Restrict management to localhost?',
        message: 'If this browser is not on the proxy machine, you will lose access after saving (unless MANAGEMENT_PASSWORD is set).',
        confirmLabel: 'Save anyway',
        danger: true
      });
      if (!ok) return;
    }
    void save(
      'mgmt',
      { management: { 'allow-remote': draft.allowRemote, 'disable-ui': draft.disableUI }, observability: { logs: { debug: draft.debug } } },
      'Saved'
    );
  }

  async function saveTailscale() {
    const users = draft.tsUsers
      .split(/[\s,]+/)
      .map((u) => u.trim())
      .filter(Boolean);
    const patch: ConfigDoc = { tailscale: { enabled: draft.tsEnabled, https: draft.tsHTTPS, 'management-users': users } };
    if (draft.tsHostname.trim()) patch.tailscale!.hostname = draft.tsHostname.trim();
    await save('ts', patch, 'Tailscale settings saved');
    void appStatus.refreshTailscale();
  }

  const stateTone = $derived(
    !ts
      ? 'neutral'
      : ts.state === 'Running'
        ? 'good'
        : ts.state === 'NeedsLogin' || ts.state === 'NeedsMachineAuth'
          ? 'warn'
          : ts.state === 'Disabled'
            ? 'neutral'
            : 'info'
  );
</script>

<PageHeader title="Settings" subtitle="Changes are written to config.yaml and apply without a restart unless noted." />

<div class="cols">
  <section class="card card-pad stack" aria-labelledby="ts-h">
    <div class="row wrap">
      <h2 id="ts-h">Tailscale</h2>
      {#if ts}<StatusBadge tone={stateTone} text={ts.state === 'Disabled' ? 'Off' : ts.state} />{/if}
      <div class="spacer"></div>
      <button class="btn ghost sm" type="button" onclick={() => appStatus.refreshTailscale()}
        ><Icon name="refresh" size={14} />Refresh</button
      >
    </div>
    {#if appStatus.tailscaleError}<Banner tone="bad">{appStatus.tailscaleError}</Banner>{/if}
    {#if ts}
      {#if ts.restart_required}
        <Banner tone="warn" title="Restart required"
          >The saved Tailscale settings differ from the running node. Restart the proxy (or its container) to apply them.</Banner
        >
      {/if}
      {#if ts.state === 'NeedsLogin' && ts.auth_url}
        <Banner tone="warn" title="Log in to Tailscale">
          This node is waiting to join your tailnet. <a href={ts.auth_url} target="_blank" rel="noopener noreferrer"
            >Open the Tailscale login page</a
          >.
        </Banner>
      {/if}
      {#if ts.state === 'NeedsMachineAuth'}
        <Banner tone="warn">Approve this machine in the Tailscale admin console.</Banner>
      {/if}
      {#if ts.active && ts.https_enabled && (ts.cert_domains ?? []).length === 0}
        <Banner tone="warn" title="HTTPS certificates unavailable"
          >Enable MagicDNS and HTTPS certificates in the Tailscale admin console, or the HTTPS address will not work.</Banner
        >
      {/if}
      {#if ts.active}
        <dl class="kv">
          {#if ts.urls?.length}
            <dt>Address</dt>
            <dd>
              {#each ts.urls as u (u)}
                <div class="row">
                  <a href={u} target="_blank" rel="noopener noreferrer">{u}</a><CopyButton text={u} label="Copy address" />
                </div>
              {/each}
            </dd>
          {/if}
          {#if ts.dns_name}<dt>Name</dt>
            <dd><code>{ts.dns_name}</code></dd>{/if}
          {#if ts.ips?.length}<dt>IPs</dt>
            <dd><code>{ts.ips.join(', ')}</code></dd>{/if}
          {#if ts.tailnet}<dt>Tailnet</dt>
            <dd>{ts.tailnet}</dd>{/if}
          {#if ts.self_user}<dt>Owner</dt>
            <dd>{ts.self_user}</dd>{/if}
          {#if ts.tags?.length}<dt>Tags</dt>
            <dd>{ts.tags.join(', ')}</dd>{/if}
          <dt>You</dt>
          <dd>
            {#if ts.caller.via_tailnet}
              {ts.caller.login_name ?? 'unknown'} on {ts.caller.node_name ?? 'unknown device'}{ts.caller.management_identity
                ? ' · trusted for management'
                : ''}
            {:else}
              Not connected through the tailnet
            {/if}
          </dd>
        </dl>
      {:else if !ts.enabled}
        <p class="muted small">
          The proxy can join your tailnet as its own machine and serve this API at <code>https://&lt;name&gt;.&lt;tailnet&gt;.ts.net</code>,
          no sidecar needed.
        </p>
      {/if}
    {/if}
    <div class="sub stack">
      <Toggle
        bind:checked={draft.tsEnabled}
        label="Join the tailnet"
        hint="Requires a restart. Provide an auth key via TS_AUTHKEY or log in with the link shown here."
      />
      <div class="grid2">
        <div class="field">
          <label for="ts-host">Machine name</label>
          <input id="ts-host" type="text" bind:value={draft.tsHostname} placeholder="cliproxy" autocomplete="off" />
        </div>
        <div class="field">
          <span class="label">HTTPS</span>
          <Toggle bind:checked={draft.tsHTTPS} label="Serve HTTPS on :443" />
        </div>
      </div>
      <div class="field">
        <label for="ts-users">Trusted management users</label>
        <textarea id="ts-users" rows="3" bind:value={draft.tsUsers} placeholder="you@github" spellcheck="false"></textarea>
        <span class="hint"
          >Tailnet logins (one per line) that can use this UI over Tailscale without the management key. Applies immediately.</span
        >
      </div>
      <div class="row">
        <span class="path">tailscale.*</span>
        <div class="spacer"></div>
        <button class="btn" type="button" disabled={!tsDirty} onclick={() => (draft = revertFields(draft, base, CARDS.ts))}>Reset</button>
        <button class="btn primary" type="button" disabled={!loaded || !tsDirty || saving !== null} onclick={saveTailscale}>Save</button>
      </div>
    </div>
  </section>

  <div class="stack">
    <section class="card card-pad stack" aria-labelledby="routing-h">
      <h2 id="routing-h">Routing</h2>
      <div class="field">
        <label for="strategy">When several accounts can serve a model</label>
        <select id="strategy" bind:value={draft.strategy}>
          <option value="round-robin">Take turns (round-robin)</option>
          <option value="weighted-round-robin">Take turns by weight</option>
          <option value="fill-first">Use the first until it is exhausted (fill-first)</option>
        </select>
      </div>
      <Toggle
        bind:checked={draft.affinity}
        label="Keep a conversation on one account"
        hint="Better prompt-cache reuse; still fails over when that account is unavailable."
      />
      <div class="grid2">
        <div class="field">
          <label for="retry">Retry rounds</label>
          <input id="retry" type="number" min="0" max="20" bind:value={draft.retry} />
          <span class="hint">Extra rounds after 403/408/429/5xx.</span>
        </div>
        <div class="field">
          <label for="retry-wait">Max wait for cooldown (s)</label>
          <input id="retry-wait" type="number" min="0" bind:value={draft.retryWait} />
          <span class="hint">0 = never wait.</span>
        </div>
      </div>
      <Toggle
        bind:checked={draft.disableCooling}
        label="Never rest failing accounts"
        hint="Disables cooldowns after rate limits and errors."
      />
      <div class="row">
        <span class="path">routing.*</span>
        <div class="spacer"></div>
        <button class="btn primary" type="button" disabled={!loaded || !routingDirty || saving !== null} onclick={saveRouting}>Save</button>
      </div>
    </section>

    <section class="card card-pad stack" aria-labelledby="usage-h">
      <h2 id="usage-h">Usage history</h2>
      <Toggle
        bind:checked={draft.usageEnabled}
        label="Record usage"
        hint="Stores tokens, latency and errors per request in a local SQLite file. No prompts or keys are stored."
      />
      <div class="field">
        <label for="retention">Keep history for (days)</label>
        <input id="retention" type="number" min="-1" bind:value={draft.retention} style="max-width:160px" />
        <span class="hint">-1 keeps everything.</span>
      </div>
      <div class="row">
        <span class="path">observability.usage.*</span>
        <div class="spacer"></div>
        <button class="btn primary" type="button" disabled={!loaded || !usageDirty || saving !== null} onclick={saveUsage}>Save</button>
      </div>
    </section>

    <section class="card card-pad stack" aria-labelledby="mgmt-h">
      <h2 id="mgmt-h">Management &amp; diagnostics</h2>
      <Toggle
        bind:checked={draft.allowRemote}
        label="Allow management from other machines"
        hint="Needed to use this UI over the LAN with the management key. Tailnet users listed above do not need it."
      />
      <Toggle bind:checked={draft.debug} label="Debug logging" hint="Verbose proxy log. Request/response logging is on the Logs page." />
      <Toggle bind:checked={draft.disableUI} label="Turn off this web UI" hint="The management API keeps working." />
      <div class="row">
        <span class="path">management.* · observability.logs.debug</span>
        <div class="spacer"></div>
        <button class="btn primary" type="button" disabled={!loaded || !mgmtDirty || saving !== null} onclick={saveMgmt}>Save</button>
      </div>
    </section>
  </div>
</div>

<section class="card card-pad stack advanced" aria-labelledby="raw-h">
  <details>
    <summary><h2 id="raw-h">Advanced: edit config.yaml</h2></summary>
    <div class="raw"><RawConfig /></div>
  </details>
</section>

<style>
  .cols {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 16px;
    align-items: start;
  }
  .stack {
    gap: 14px;
  }
  .sub {
    border-top: 1px solid var(--border);
    padding-top: 14px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .label {
    font-weight: 500;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 6px 14px;
    margin: 0;
  }
  .kv dt {
    color: var(--text-muted);
  }
  .kv dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .advanced {
    margin-top: 16px;
  }
  summary {
    cursor: pointer;
    list-style: revert;
  }
  summary h2 {
    display: inline;
  }
  .raw {
    margin-top: 14px;
  }
  @media (max-width: 1000px) {
    .cols {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  @media (max-width: 520px) {
    .grid2 {
      grid-template-columns: 1fr;
    }
  }
</style>
