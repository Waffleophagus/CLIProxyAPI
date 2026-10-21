<script lang="ts">
  import { onMount } from 'svelte';
  import { auth } from './lib/auth.svelte';
  import { config } from './lib/config.svelte';
  import { appStatus } from './lib/status.svelte';
  import { router, routes, link } from './lib/router.svelte';
  import { theme } from './lib/theme.svelte';
  import Icon from './components/Icon.svelte';
  import Overview from './pages/Overview.svelte';
  import Slugs from './pages/Slugs.svelte';
  import Accounts from './pages/Accounts.svelte';
  import ClientKeys from './pages/ClientKeys.svelte';
  import Usage from './pages/Usage.svelte';
  import Logs from './pages/Logs.svelte';
  import Settings from './pages/Settings.svelte';

  const icons: Record<string, string> = {
    '/': 'overview',
    '/slugs': 'slug',
    '/accounts': 'accounts',
    '/keys': 'key',
    '/usage': 'usage',
    '/logs': 'logs',
    '/settings': 'settings'
  };

  // The mobile menu closes after navigating: a writable derived resets to false
  // whenever the route changes, and the menu button overrides it locally.
  let navOpen = $derived.by(() => {
    void router.path;
    return false;
  });

  onMount(() => {
    void config.load();
  });

  const current = $derived(routes.find((r) => r.path === router.path) ?? null);
  $effect(() => {
    document.title = current ? `${current.label} · CLIProxy` : 'CLIProxy';
  });

  const usageOff = $derived(appStatus.usageDisabled || (config.data !== null && !config.usageEnabled));
  const restartRequired = $derived(auth.tailscale?.restart_required === true);
  const themeIcon = $derived(theme.choice === 'light' ? 'sun' : theme.choice === 'dark' ? 'moon' : 'monitor');
  const themeLabel = $derived(`Theme: ${theme.choice} (click to change)`);
</script>

<div class={['layout', { 'nav-open': navOpen }]}>
  <aside class="sidebar" aria-label="Main">
    <div class="brand">
      <img src="/ui/favicon.svg" alt="" width="26" height="26" />
      <span>CLIProxy</span>
    </div>
    <nav>
      <ul>
        {#each routes as r (r.path)}
          <li>
            <a
              href={router.href(r.path)}
              use:link
              class={{ active: router.path === r.path }}
              aria-current={router.path === r.path ? 'page' : undefined}
            >
              <Icon name={icons[r.path]} size={17} />
              <span>{r.label}</span>
            </a>
          </li>
        {/each}
      </ul>
    </nav>
    <div class="side-foot">
      {#if auth.identity}
        <div class="who small" title="Signed in by Tailscale identity">
          <Icon name="shield" size={14} /><span class="truncate">{auth.identity}</span>
        </div>
      {:else}
        <button class="btn ghost sm" type="button" onclick={() => auth.signOut()}><Icon name="logout" size={14} />Sign out</button>
      {/if}
    </div>
  </aside>
  <button class="scrim" type="button" aria-label="Close menu" tabindex="-1" onclick={() => (navOpen = false)}></button>

  <div class="main">
    <header class="topbar">
      <button
        class="btn ghost icon menu-btn"
        type="button"
        aria-label="Open menu"
        aria-expanded={navOpen}
        onclick={() => (navOpen = !navOpen)}
      >
        <Icon name="menu" />
      </button>
      <div class="conn">
        <span class={['dot', { bad: config.error !== '' }]} aria-hidden="true"></span>
        {#if config.error}
          <span>Connection problem: {config.error}</span>
        {:else if auth.identity}
          <span>Signed in via Tailscale as <strong>{auth.identity}</strong></span>
        {:else}
          <span>Connected</span>
        {/if}
      </div>
      <div class="spacer"></div>
      <button class="btn ghost icon" type="button" onclick={() => theme.cycle()} aria-label={themeLabel} title={themeLabel}>
        <Icon name={themeIcon} />
      </button>
    </header>

    <div class="alerts">
      {#if config.exampleKeys.length > 0}
        <div class="alert bad" role="alert">
          <Icon name="alert" />
          <span
            ><strong>Example client keys are configured.</strong> Anyone can use these well-known keys, and a proxy started with them refuses
            all requests (safe mode).</span
          >
          <a class="btn sm" href={router.href('/keys')} use:link>Fix client keys</a>
        </div>
      {/if}
      {#if restartRequired}
        <div class="alert warn">
          <Icon name="refresh" />
          <span><strong>Restart required:</strong> Tailscale settings changed and take effect after the proxy restarts.</span>
          <a class="btn sm" href={router.href('/settings')} use:link>Details</a>
        </div>
      {/if}
      {#if usageOff}
        <div class="alert info">
          <Icon name="info" />
          <span>Usage tracking is off, so charts and request history are empty.</span>
          <a class="btn sm" href={router.href('/settings')} use:link>Turn on</a>
        </div>
      {/if}
    </div>

    <main class="content" id="content">
      {#if router.path === '/'}
        <Overview />
      {:else if router.path === '/slugs'}
        <Slugs />
      {:else if router.path === '/accounts'}
        <Accounts />
      {:else if router.path === '/keys'}
        <ClientKeys />
      {:else if router.path === '/usage'}
        <Usage />
      {:else if router.path === '/logs'}
        <Logs />
      {:else if router.path === '/settings'}
        <Settings />
      {:else}
        <div class="empty">
          <h1>Page not found</h1>
          <p><a href={router.href('/')} use:link>Go to the overview</a></p>
        </div>
      {/if}
    </main>
  </div>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: 216px minmax(0, 1fr);
    min-height: 100dvh;
  }
  .sidebar {
    position: sticky;
    top: 0;
    height: 100dvh;
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 16px 10px;
    border-right: 1px solid var(--border);
    background: var(--surface);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    font-weight: 600;
    font-size: 1.05rem;
    padding: 2px 8px 14px;
  }
  nav ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  nav a {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 10px;
    border-radius: var(--radius-sm);
    color: var(--text-2);
    font-weight: 500;
  }
  nav a:hover {
    background: var(--surface-hover);
    color: var(--text);
    text-decoration: none;
  }
  nav a.active {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .side-foot {
    margin-top: auto;
    padding: 8px;
  }
  .who {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-muted);
    min-width: 0;
  }
  .main {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .topbar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 24px;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
    z-index: 20;
  }
  .conn {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-2);
    font-size: 0.9rem;
    min-width: 0;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--good);
    flex: none;
  }
  .dot.bad {
    background: var(--bad);
  }
  .alerts {
    display: flex;
    flex-direction: column;
  }
  .alert {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 24px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .alert span {
    flex: 1;
    min-width: 200px;
  }
  .alert.bad {
    background: var(--bad-soft);
  }
  .alert.bad > :global(svg) {
    color: var(--bad-text);
  }
  .alert.warn {
    background: var(--warn-soft);
  }
  .alert.warn > :global(svg) {
    color: var(--warn-text);
  }
  .alert.info {
    background: var(--accent-soft);
  }
  .alert.info > :global(svg) {
    color: var(--accent-text);
  }
  .content {
    padding: 24px;
    max-width: 1280px;
    width: 100%;
  }
  .menu-btn,
  .scrim {
    display: none;
  }
  @media (max-width: 860px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
    .sidebar {
      position: fixed;
      left: 0;
      top: 0;
      z-index: 50;
      width: 240px;
      transform: translateX(-100%);
      transition: transform 0.18s ease;
    }
    .nav-open .sidebar {
      transform: none;
      box-shadow: var(--shadow-pop);
    }
    .nav-open .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 40;
      background: rgba(0, 0, 0, 0.3);
      border: 0;
    }
    .menu-btn {
      display: inline-flex;
    }
    .topbar,
    .alert {
      padding-left: 12px;
      padding-right: 12px;
    }
    .content {
      padding: 16px 12px;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sidebar {
      transition: none;
    }
  }
</style>
