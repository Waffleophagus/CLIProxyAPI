<script lang="ts" module>
  export type OAuthProvider = { id: string; label: string; note: string };

  export const OAUTH_PROVIDERS: OAuthProvider[] = [
    { id: 'claude', label: 'Claude', note: 'Claude Pro / Max subscription (Anthropic sign-in).' },
    { id: 'codex', label: 'Codex', note: 'ChatGPT Plus / Pro / Team account (OpenAI sign-in).' },
    { id: 'xai', label: 'xAI', note: 'Grok account; approve with a code.' },
    { id: 'meta', label: 'Meta', note: 'Meta AI account; approve with a code.' }
  ];
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { api, errorMessage } from '../../lib/api';
  import { toasts } from '../../lib/toast.svelte';
  import { OAuthFlow } from '../../lib/oauth-flow';
  import type { OAuthStart, OAuthStatus } from '../../lib/types';
  import Dialog from '../../components/Dialog.svelte';
  import Banner from '../../components/Banner.svelte';
  import CopyButton from '../../components/CopyButton.svelte';
  import Icon from '../../components/Icon.svelte';

  let {
    open = $bindable(false),
    provider = '',
    siwcName = '',
    ondone
  }: {
    open?: boolean;
    /** Preselected provider id; empty shows the chooser. */
    provider?: string;
    /** When set, binds Sign in with ChatGPT to this Codex credential instead. */
    siwcName?: string;
    ondone: () => void;
  } = $props();

  type Phase = 'choose' | 'starting' | 'waiting' | 'done' | 'error';
  let phase = $state<Phase>('choose');
  let chosen = $state('');
  let start = $state<OAuthStart | null>(null);
  let error = $state('');
  let pasted = $state('');
  let submitting = $state(false);

  const isDevice = $derived(start?.flow === 'device');
  const isSIWC = $derived(siwcName !== '');
  const title = $derived(
    isSIWC
      ? 'Bind Sign in with ChatGPT'
      : phase === 'choose'
        ? 'Add account'
        : `Add ${OAUTH_PROVIDERS.find((p) => p.id === chosen)?.label ?? chosen} account`
  );

  const flow = new OAuthFlow(
    {
      start: () =>
        isSIWC
          ? api.post<OAuthStart>('oauth/siwc/bind', { name: siwcName })
          : api.get<OAuthStart>('oauth/auth-url', { query: { provider: chosen, is_webui: 'true' } }),
      status: (state, signal) => api.get<OAuthStatus>('oauth/status', { query: { state }, signal }),
      cancel: (state) => api.del('oauth/session', { query: { state } }),
      every: (fn, ms) => {
        const t = setInterval(fn, ms);
        return () => clearInterval(t);
      }
    },
    {
      waiting: (s) => {
        start = s;
        phase = 'waiting';
      },
      done: () => {
        phase = 'done';
        ondone();
      },
      failed: (message) => {
        error = message;
        phase = 'error';
      },
      pollError: (message) => (error = message)
    }
  );

  // Closing (any way) or unmounting invalidates in-flight requests, stops
  // polling and cancels a pending server session.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      error = '';
      pasted = '';
      start = null;
      if (isSIWC) {
        chosen = 'codex';
        begin();
      } else if (provider) {
        chosen = provider;
        begin();
      } else {
        phase = 'choose';
      }
    });
    return () => flow.abandon();
  });

  function begin() {
    phase = 'starting';
    error = '';
    start = null;
    void flow.begin();
  }

  async function submitRedirect(e: SubmitEvent) {
    e.preventDefault();
    const state = flow.session;
    if (!state || !pasted.trim()) return;
    submitting = true;
    try {
      await api.post('oauth/callback', { redirect_url: pasted.trim(), state, ...(isSIWC ? { provider: 'codex' } : {}) });
      if (flow.session !== state) return;
      toasts.info('Redirect accepted; finishing sign-in…');
      await flow.check();
    } catch (err) {
      if (flow.session === state) error = errorMessage(err);
    } finally {
      submitting = false;
    }
  }

  function close() {
    open = false;
  }
</script>

<Dialog bind:open {title} onclose={() => flow.abandon()}>
  {#if phase === 'choose'}
    <div class="stack">
      <p class="muted">Pick the service to sign in to. The proxy stores the resulting account in its auth directory.</p>
      <div class="choices">
        {#each OAUTH_PROVIDERS as p (p.id)}
          <button
            type="button"
            class="choice"
            onclick={() => {
              chosen = p.id;
              begin();
            }}
          >
            <strong>{p.label}</strong>
            <span class="muted small">{p.note}</span>
          </button>
        {/each}
      </div>
    </div>
  {:else if phase === 'starting'}
    <p class="muted">Starting sign-in…</p>
  {:else if phase === 'waiting' && start}
    <div class="stack">
      {#if isDevice}
        <ol class="steps">
          <li>
            <span>Copy this code:</span>
            <div class="code-box">
              <code class="code">{start.user_code ?? '—'}</code>
              <CopyButton text={start.user_code ?? ''} label="Copy code" showLabel small={false} />
            </div>
          </li>
          <li>
            <span>Open the verification page and enter the code:</span>
            <a class="btn primary" href={start.url} target="_blank" rel="noopener noreferrer"
              ><Icon name="external" />Open verification page</a
            >
          </li>
          <li><span>Approve the sign-in. This dialog updates by itself.</span></li>
        </ol>
      {:else}
        <ol class="steps">
          <li>
            <span>{isSIWC ? 'Open the ChatGPT authorization page and approve.' : 'Open the sign-in page and approve access.'}</span>
            <div class="row wrap">
              <a class="btn primary" href={start.url} target="_blank" rel="noopener noreferrer"><Icon name="external" />Open sign-in page</a
              >
              <CopyButton text={start.url} label="Copy sign-in link" showLabel />
            </div>
          </li>
          <li>
            <span>After approving, you are sent to a <code>localhost</code> address.</span>
            {#if isSIWC}
              <span class="muted small"
                >That page will not load (nothing listens there). Copy the full address from the browser's address bar and paste it below.</span
              >
            {:else}
              <span class="muted small"
                >If this browser runs on the proxy's machine, sign-in finishes automatically. Otherwise that page will not load: copy the
                full address from the address bar and paste it below.</span
              >
            {/if}
          </li>
        </ol>
        <form class="stack paste" onsubmit={submitRedirect}>
          <div class="field">
            <label for="redirect-url">Redirect URL</label>
            <input
              id="redirect-url"
              type="url"
              bind:value={pasted}
              placeholder="http://localhost:…/callback?code=…&state=…"
              autocomplete="off"
            />
          </div>
          <button class="btn" type="submit" disabled={!pasted.trim() || submitting}
            >{submitting ? 'Submitting…' : 'Submit redirect URL'}</button
          >
        </form>
      {/if}
      {#if error}<Banner tone="bad">{error}</Banner>{/if}
      <p class="small muted waiting"><span class="pulse" aria-hidden="true"></span>Waiting for approval…</p>
    </div>
  {:else if phase === 'done'}
    <Banner tone="good" title="Signed in"
      >{isSIWC ? 'Sign in with ChatGPT is now bound to this account.' : 'The account was added and is ready to use.'}</Banner
    >
  {:else if phase === 'error'}
    <div class="stack">
      <Banner tone="bad" title="Sign-in did not complete">{error}</Banner>
      <button class="btn" type="button" onclick={begin}><Icon name="refresh" />Try again</button>
    </div>
  {/if}
  {#snippet footer()}
    {#if phase === 'done'}
      <button class="btn primary" type="button" onclick={close}>Done</button>
    {:else}
      <button class="btn" type="button" onclick={close}>Cancel</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .choices {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 8px;
  }
  .choice {
    display: flex;
    flex-direction: column;
    gap: 2px;
    text-align: left;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text);
    font: inherit;
    cursor: pointer;
  }
  .choice:hover {
    background: var(--surface-hover);
  }
  .steps {
    margin: 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .steps li > span {
    display: block;
    margin-bottom: 6px;
  }
  .code-box {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
  }
  .code {
    font-size: 1.6rem;
    letter-spacing: 0.12em;
    font-weight: 600;
    padding: 6px 14px;
    border-radius: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .paste {
    padding-top: 12px;
    border-top: 1px solid var(--border);
    gap: 8px;
  }
  .paste .btn {
    align-self: flex-start;
  }
  .waiting {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pulse {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent);
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.25;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }
</style>
