<script lang="ts">
  // Searchable combobox over the models.dev price catalog (server-side search).
  import { errorMessage } from '../lib/api';
  import { perM } from '../lib/format';
  import { searchCatalog } from '../lib/usage';
  import type { CatalogModel } from '../lib/types';

  let {
    value = $bindable(''),
    label = 'Price as',
    onpick
  }: {
    /** Selected models.dev ID ("provider/model"). */
    value?: string;
    label?: string;
    onpick?: (m: CatalogModel) => void;
  } = $props();

  const id = `mp-${Math.random().toString(36).slice(2, 9)}`;
  let query = $derived(value);
  let results = $state<CatalogModel[]>([]);
  let open = $state(false);
  let active = $state(-1);
  let error = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  function search(q: string) {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const mine = ++seq;
      try {
        const r = await searchCatalog(q, 30);
        if (mine !== seq) return;
        results = r.models;
        active = results.length ? 0 : -1;
        error = '';
      } catch (e) {
        if (mine !== seq) return;
        results = [];
        error = errorMessage(e);
      }
    }, 150);
  }

  function oninput(e: Event) {
    query = (e.currentTarget as HTMLInputElement).value;
    open = true;
    search(query);
  }

  function pick(m: CatalogModel) {
    value = m.id;
    query = m.id;
    open = false;
    onpick?.(m);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!open) {
        open = true;
        search(query);
      } else if (results.length) active = (active + 1) % results.length;
    } else if (e.key === 'ArrowUp' && open && results.length) {
      e.preventDefault();
      active = (active - 1 + results.length) % results.length;
    } else if (e.key === 'Enter' && open && active >= 0 && results[active]) {
      e.preventDefault();
      pick(results[active]);
    } else if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      open = false;
    }
  }

  function onfocus() {
    open = true;
    search(query);
  }

  function onblur() {
    // Let a click on an option land before the list closes.
    setTimeout(() => (open = false), 120);
  }
</script>

<div class="picker">
  <label for={id}>{label}</label>
  <input
    {id}
    type="text"
    role="combobox"
    autocomplete="off"
    spellcheck="false"
    placeholder="Search models.dev, e.g. glm 5.3 flash"
    aria-expanded={open}
    aria-controls="{id}-list"
    aria-autocomplete="list"
    aria-activedescendant={open && active >= 0 ? `${id}-opt-${active}` : undefined}
    value={query}
    {oninput}
    {onkeydown}
    {onfocus}
    {onblur}
  />
  {#if open}
    <ul id="{id}-list" role="listbox" aria-label="{label} options">
      {#if error}
        <li class="note bad">{error}</li>
      {:else if results.length === 0}
        <li class="note">No matching models.</li>
      {/if}
      {#each results as m, i (m.id)}
        <!-- Keyboard selection is handled by the combobox input (aria-activedescendant). -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <li
          id="{id}-opt-{i}"
          role="option"
          aria-selected={i === active}
          class={{ active: i === active }}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => pick(m)}
          onmouseenter={() => (active = i)}
        >
          <span class="name">{m.name}</span>
          <span class="meta"><code>{m.id}</code> · {perM(m.price.input)} in / {perM(m.price.output)} out per 1M</span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .picker {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  label {
    font-weight: 500;
    font-size: 0.86rem;
    color: var(--text-2);
  }
  ul {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    z-index: 20;
    margin: 4px 0 0;
    padding: 4px;
    list-style: none;
    max-height: 300px;
    overflow: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: var(--shadow-pop);
  }
  li {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 6px 8px;
    border-radius: 6px;
    cursor: pointer;
  }
  li.active {
    background: var(--surface-hover);
  }
  .name {
    font-weight: 500;
  }
  .meta {
    font-size: 0.8rem;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }
  .note {
    cursor: default;
    color: var(--text-muted);
    font-size: 0.86rem;
  }
  .note.bad {
    color: var(--bad-text);
  }
</style>
