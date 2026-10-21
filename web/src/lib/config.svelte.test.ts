import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type Call = { method: string; url: string; body: unknown; ifMatch: string | null };

// A fake management API holding one config version at a time.
function fakeServer(initial: { keys: string[]; etag: string }) {
  const state = { ...initial };
  const calls: Call[] = [];
  let gate: Promise<void> | null = null;
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    const method = init.method ?? 'GET';
    const headers = (init.headers ?? {}) as Record<string, string>;
    calls.push({ method, url, body: init.body ? JSON.parse(init.body as string) : undefined, ifMatch: headers['If-Match'] ?? null });
    if (method === 'GET') {
      if (gate) await gate;
      return new Response(JSON.stringify({ access: { 'api-keys': state.keys } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json', ETag: state.etag }
      });
    }
    if (headers['If-Match'] && headers['If-Match'] !== state.etag) {
      return new Response(JSON.stringify({ error: 'precondition_failed' }), {
        status: 412,
        headers: { 'Content-Type': 'application/json' }
      });
    }
    if (method === 'PUT' && url.endsWith('/config/access/api-keys')) {
      state.keys = JSON.parse(init.body as string) as string[];
      state.etag = `"v${Math.random()}"`;
    }
    return new Response(JSON.stringify({ status: 'ok' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  return {
    state,
    calls,
    fetchMock,
    hold() {
      let release!: () => void;
      gate = new Promise((r) => (release = r));
      return () => {
        gate = null;
        release();
      };
    }
  };
}

async function freshStore() {
  vi.resetModules();
  return import('./config.svelte');
}

describe('config store writes', () => {
  let server: ReturnType<typeof fakeServer>;
  beforeEach(() => {
    server = fakeServer({ keys: ['sk-existing-1', 'sk-existing-2'], etag: '"v1"' });
    vi.stubGlobal('fetch', server.fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('refuses a write computed before the config loaded instead of sending it with a fresh ETag', async () => {
    const { config } = await freshStore();
    const release = server.hold();
    const loading = config.load();
    // What the Client keys page computed while config.data was still null.
    const write = config.put('access/api-keys', ['sk-new']);
    release();
    await loading;
    await expect(write).rejects.toThrow();
    expect(server.calls.filter((c) => c.method === 'PUT')).toHaveLength(0);
    expect(server.state.keys).toEqual(['sk-existing-1', 'sk-existing-2']);
  });

  it('sends the If-Match of the snapshot the value was computed from', async () => {
    const { config, ConflictError } = await freshStore();
    await config.load();
    const snap = config.snapshot()!;
    expect(snap.etag).toBe('"v1"');
    // Another writer saves; this tab reloads before the user confirms.
    server.state.keys = ['sk-other'];
    server.state.etag = '"v2"';
    await config.load();
    const next = [...(snap.doc.access?.['api-keys'] ?? []), 'sk-new'];
    await expect(config.put('access/api-keys', next, snap)).rejects.toBeInstanceOf(ConflictError);
    const put = server.calls.find((c) => c.method === 'PUT')!;
    expect(put.ifMatch).toBe('"v1"');
    expect(server.state.keys).toEqual(['sk-other']);
  });

  it('writes a value derived from a loaded snapshot and reloads', async () => {
    const { config } = await freshStore();
    await config.load();
    const snap = config.snapshot()!;
    await config.put('access/api-keys', [...(snap.doc.access?.['api-keys'] ?? []), 'sk-new'], snap);
    expect(server.state.keys).toEqual(['sk-existing-1', 'sk-existing-2', 'sk-new']);
    expect(config.data?.access?.['api-keys']).toEqual(server.state.keys);
    expect(config.etag).toBe(server.state.etag);
  });

  it('snapshot is a detached copy', async () => {
    const { config } = await freshStore();
    expect(config.snapshot()).toBeNull();
    await config.load();
    const snap = config.snapshot()!;
    snap.doc.access!['api-keys']!.push('mutated');
    expect(config.data?.access?.['api-keys']).toEqual(['sk-existing-1', 'sk-existing-2']);
  });
});
