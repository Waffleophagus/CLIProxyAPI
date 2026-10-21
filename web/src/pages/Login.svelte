<script lang="ts">
  import { auth } from '../lib/auth.svelte';
  import Banner from '../components/Banner.svelte';
  import Icon from '../components/Icon.svelte';

  let key = $state('');
  let remember = $state(false);
  let busy = $state(false);
  let reveal = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!key.trim()) return;
    busy = true;
    const ok = await auth.signIn(key, remember);
    busy = false;
    if (ok) key = '';
  }
</script>

<main class="login">
  <div class="card panel">
    <div class="brand">
      <img src="/ui/favicon.svg" alt="" width="32" height="32" />
      <div>
        <h1>CLIProxy</h1>
        <p class="muted small">Management</p>
      </div>
    </div>

    {#if auth.phase === 'disabled'}
      <Banner tone="warn" title="Management API is turned off">
        <p>
          This proxy is running, but its management API is not enabled. Set
          <code>management.secret-key</code> in <code>config.yaml</code> (or the <code>MANAGEMENT_PASSWORD</code> environment variable) and reload
          this page.
        </p>
        <p class="small muted" style="margin-top:6px">
          If only <code>tailscale.management-users</code> is set, the API is available just to those users over the tailnet.
        </p>
      </Banner>
      <button class="btn" type="button" onclick={() => auth.init()}><Icon name="refresh" />Try again</button>
    {:else}
      <form onsubmit={submit} class="stack">
        <p class="muted">Enter the management key from <code>management.secret-key</code> (or <code>MANAGEMENT_PASSWORD</code>).</p>
        {#if auth.message}
          <Banner tone="bad">{auth.message}</Banner>
        {/if}
        <div class="field">
          <label for="mgmt-key">Management key</label>
          <div class="keyrow">
            <!-- svelte-ignore a11y_autofocus -->
            <input id="mgmt-key" type={reveal ? 'text' : 'password'} bind:value={key} autocomplete="current-password" autofocus required />
            <button type="button" class="btn ghost icon" onclick={() => (reveal = !reveal)} aria-label={reveal ? 'Hide key' : 'Show key'}>
              <Icon name={reveal ? 'eyeOff' : 'eye'} />
            </button>
          </div>
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={remember} />
          <span>Remember on this device</span>
        </label>
        <p class="small muted">
          {remember ? 'The key is stored in this browser until you sign out.' : 'The key is kept only for this browser tab.'}
        </p>
        <button class="btn primary" type="submit" disabled={busy || !key.trim()}>
          <Icon name="login" />{busy ? 'Checking…' : 'Sign in'}
        </button>
        <p class="small muted">Over Tailscale, users listed in <code>tailscale.management-users</code> are signed in automatically.</p>
      </form>
    {/if}
  </div>
</main>

<style>
  .login {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: 24px 16px;
  }
  .panel {
    width: min(440px, 100%);
    padding: 28px;
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  .brand {
    display: flex;
    gap: 12px;
    align-items: center;
  }
  .keyrow {
    display: flex;
    gap: 4px;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    font-weight: 400;
  }
</style>
