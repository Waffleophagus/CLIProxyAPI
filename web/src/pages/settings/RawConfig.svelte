<script lang="ts">
  import { ApiError, errorMessage, request } from '../../lib/api';
  import { config } from '../../lib/config.svelte';
  import { confirm } from '../../lib/confirm.svelte';
  import { toasts } from '../../lib/toast.svelte';
  import Banner from '../../components/Banner.svelte';
  import Icon from '../../components/Icon.svelte';

  let text = $state('');
  let original = $state('');
  let etag = $state<string | null>(null);
  let loaded = $state(false);
  let loading = $state(false);
  let saving = $state(false);
  let error = $state('');
  let conflict = $state(false);

  const dirty = $derived(loaded && text !== original);

  async function load() {
    if (
      dirty &&
      !(await confirm({
        title: 'Discard your edits?',
        message: 'Reloading replaces the editor content with the file on disk.',
        confirmLabel: 'Discard and reload',
        danger: true
      }))
    )
      return;
    loading = true;
    error = '';
    conflict = false;
    try {
      const r = await request<string>('GET', 'config.yaml', { headers: { Accept: 'application/yaml, text/plain, */*' } });
      text = original = typeof r.data === 'string' ? r.data : '';
      etag = r.etag;
      loaded = true;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  async function save(force = false) {
    saving = true;
    error = '';
    try {
      const headers: Record<string, string> = {};
      if (!force && etag) headers['If-Match'] = etag;
      const r = await request('PUT', 'config.yaml', { rawBody: text, contentType: 'application/yaml', headers });
      conflict = false;
      toasts.ok('config.yaml saved; changes apply automatically');
      etag = r.etag;
      original = text;
      await config.load();
      // Re-read to pick up the server's normalized layout.
      const fresh = await request<string>('GET', 'config.yaml', { headers: { Accept: 'application/yaml, text/plain, */*' } });
      text = original = typeof fresh.data === 'string' ? fresh.data : text;
      etag = fresh.etag;
    } catch (e) {
      if (e instanceof ApiError && e.status === 412) {
        conflict = true;
        error = 'config.yaml changed on the server after you loaded it (another tab, the UI elsewhere, or a file edit).';
      } else {
        error = errorMessage(e);
      }
    } finally {
      saving = false;
    }
  }

  async function overwrite() {
    const ok = await confirm({
      title: 'Overwrite newer config?',
      message: 'Your editor content replaces the newer version on the server. Changes made elsewhere since you loaded will be lost.',
      confirmLabel: 'Overwrite',
      danger: true
    });
    if (ok) void save(true);
  }

  function onKey(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      if (dirty && !saving) void save();
    }
  }
</script>

<div class="stack">
  {#if !loaded}
    <div class="row wrap">
      <p class="muted">Edit the full configuration file directly. Mistakes can break the proxy; the server validates before saving.</p>
      <button class="btn" type="button" onclick={load} disabled={loading}
        ><Icon name="edit" />{loading ? 'Loading…' : 'Open config.yaml'}</button
      >
    </div>
    {#if error}<Banner tone="bad">{error}</Banner>{/if}
  {:else}
    {#if error}
      <Banner tone="bad" title={conflict ? 'Edit conflict' : 'Not saved'}>
        <p>{error}</p>
        {#if conflict}
          <div class="row wrap" style="margin-top:8px">
            <button class="btn sm" type="button" onclick={load}>Discard mine and reload</button>
            <button class="btn sm danger" type="button" onclick={overwrite}>Overwrite with mine</button>
          </div>
        {/if}
      </Banner>
    {/if}
    <label for="raw-yaml" class="sr-only">config.yaml</label>
    <textarea id="raw-yaml" class="yaml" bind:value={text} spellcheck="false" autocomplete="off" onkeydown={onKey}></textarea>
    <div class="row wrap">
      <span class="small muted"
        >{dirty ? 'Unsaved changes' : 'No changes'} · Ctrl+S saves · secrets in this file are shown in plain text</span
      >
      <div class="spacer"></div>
      <button class="btn" type="button" onclick={load} disabled={loading || saving}><Icon name="refresh" />Reload</button>
      <button class="btn" type="button" onclick={() => (text = original)} disabled={!dirty}>Revert</button>
      <button class="btn primary" type="button" onclick={() => save()} disabled={!dirty || saving}
        >{saving ? 'Saving…' : 'Save config.yaml'}</button
      >
    </div>
  {/if}
</div>

<style>
  .yaml {
    font-family: var(--mono);
    font-size: 0.82rem;
    line-height: 1.5;
    min-height: 420px;
    height: 60vh;
    white-space: pre;
    tab-size: 2;
  }
</style>
