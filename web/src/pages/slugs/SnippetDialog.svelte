<script lang="ts">
  import Dialog from '../../components/Dialog.svelte';
  import CopyButton from '../../components/CopyButton.svelte';
  import { clientBaseURL } from '../../lib/slugs';
  import { router, link } from '../../lib/router.svelte';

  let { open = $bindable(false), alias }: { open?: boolean; alias: string } = $props();

  const base = $derived(clientBaseURL());
  const curl = $derived(
    `curl ${base}/chat/completions \\\n  -H "Authorization: Bearer $CLIPROXY_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${alias}", "messages": [{"role": "user", "content": "Hello"}]}'`
  );
  const env = $derived(`OPENAI_BASE_URL=${base}\nOPENAI_API_KEY=<your client key>\n# model: ${alias}`);
</script>

<Dialog bind:open title="Use {alias}" wide>
  <div class="stack">
    <p class="muted">Point any OpenAI-compatible client at this proxy and request the model by its slug.</p>
    <div class="kv">
      <span class="muted">Base URL</span>
      <span class="row"><code>{base}</code><CopyButton text={base} label="Copy base URL" /></span>
      <span class="muted">Model</span>
      <span class="row"><code>{alias}</code><CopyButton text={alias} label="Copy model name" /></span>
      <span class="muted">API key</span>
      <span>Any of your <a href={router.href('/keys')} use:link onclick={() => (open = false)}>client keys</a></span>
    </div>
    <div>
      <div class="row">
        <h3>curl</h3>
        <div class="spacer"></div>
        <CopyButton text={curl} label="Copy curl command" showLabel />
      </div>
      <pre class="log">{curl}</pre>
    </div>
    <div>
      <div class="row">
        <h3>Environment (OpenAI SDKs)</h3>
        <div class="spacer"></div>
        <CopyButton text={env} label="Copy environment" showLabel />
      </div>
      <pre class="log">{env}</pre>
    </div>
  </div>
</Dialog>

<style>
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    align-items: center;
  }
  h3 {
    margin: 0 0 6px;
  }
</style>
