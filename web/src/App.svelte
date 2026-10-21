<script lang="ts">
  import { onMount } from 'svelte';
  import { auth } from './lib/auth.svelte';
  import { theme } from './lib/theme.svelte';
  import { router } from './lib/router.svelte';
  import Login from './pages/Login.svelte';
  import Shell from './Shell.svelte';
  import Toasts from './components/Toasts.svelte';
  import ConfirmHost from './components/ConfirmHost.svelte';

  onMount(() => {
    theme.init();
    router.init();
    void auth.init();
  });
</script>

{#if auth.phase === 'checking'}
  <div class="splash" aria-busy="true"><span class="muted">Connecting…</span></div>
{:else if auth.phase === 'ready'}
  <Shell />
{:else}
  <Login />
{/if}
<Toasts />
<ConfirmHost />

<style>
  .splash {
    min-height: 100dvh;
    display: grid;
    place-items: center;
  }
</style>
