<script lang="ts">
  // Pops up when a model on your own hardware (e.g. the model behind a Spark
  // slug after a swap) has no price. Each model is suggested once; "Not now"
  // leaves its requests unpriced until a price is added under Usage > Pricing.
  import { onMount } from 'svelte';
  import { router } from '../lib/router.svelte';
  import { storageGet, storageSet } from '../lib/storage';
  import { unpricedLocalModels } from '../lib/usage';
  import type { UnpricedLocalModel } from '../lib/types';
  import Dialog from './Dialog.svelte';

  const DISMISSED_KEY = 'cliproxy.unpricedSparkDismissed';
  const CHECK_EVERY_MS = 5 * 60_000;

  let pending = $state<UnpricedLocalModel[]>([]);
  let open = $state(false);

  const id = (m: UnpricedLocalModel) => `${m.provider}|${m.model}`;

  function dismissed(): Set<string> {
    try {
      const v: unknown = JSON.parse(storageGet('local', DISMISSED_KEY) ?? '[]');
      return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []);
    } catch {
      return new Set();
    }
  }

  async function check() {
    if (open) return;
    try {
      const { models } = await unpricedLocalModels();
      const seen = dismissed();
      // Several aliases can serve the same model; suggest each model once.
      pending = models.filter((m) => {
        if (seen.has(id(m))) return false;
        seen.add(id(m));
        return true;
      });
      open = pending.length > 0;
    } catch {
      // Pricing or usage tracking is off; nothing to suggest.
    }
  }

  function remember(models: UnpricedLocalModel[]) {
    const seen = dismissed();
    for (const m of models) seen.add(id(m));
    storageSet('local', DISMISSED_KEY, JSON.stringify([...seen]));
  }

  /** "Not now" (or closing): stop suggesting every listed model. */
  function dismiss() {
    remember(pending);
    pending = [];
    open = false;
  }

  /** Prices the first model; the others stay eligible for a later prompt. */
  function setPrice() {
    const first = pending[0];
    if (first) remember([first]);
    pending = [];
    open = false;
    if (first) router.go('/usage', { pricing: first.model });
  }

  onMount(() => {
    void check();
    const timer = setInterval(() => void check(), CHECK_EVERY_MS);
    return () => clearInterval(timer);
  });
</script>

<Dialog bind:open title={pending.length === 1 ? 'This model has no price yet' : 'These models have no price yet'} onclose={dismiss}>
  <p>
    {pending.length === 1 ? 'A model' : 'Some models'} on your own hardware {pending.length === 1 ? 'has' : 'have'} no price, so
    {pending.length === 1 ? 'its' : 'their'} requests won't count toward Hardware value:
  </p>
  <ul>
    {#each pending as m (id(m))}
      <li>
        {#if m.alias && m.alias !== m.model}<code>{m.alias}</code><span> now serves </span>{/if}<code>{m.model}</code>
        <span class="muted small">({m.group})</span>
      </li>
    {/each}
  </ul>
  <p class="muted small">
    When you swap the model behind a Spark slug, add a price for the new one. Earlier requests keep the old model's price.
  </p>
  {#snippet footer()}
    <button class="btn" type="button" onclick={dismiss}>Not now</button>
    <button class="btn primary" type="button" onclick={setPrice}>Set price</button>
  {/snippet}
</Dialog>

<style>
  ul {
    margin: 8px 0 12px;
    padding-left: 20px;
  }
  li {
    margin: 4px 0;
  }
</style>
