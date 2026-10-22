// Shared view of the persisted configuration. Every write sends an ETag as
// If-Match; on a conflict the store reloads and the caller gets a
// ConflictError so it can tell the user to review and retry.
//
// Writes never load implicitly: a value computed before the config arrived
// (for example from an empty list) must not be sent with a fresh ETag, or it
// would silently replace data the user never saw. Callers that derive a value
// from the document take a snapshot() and pass it to the write, so the value
// and its If-Match come from the same version.
import { ApiError, request } from './api';
import { toasts } from './toast.svelte';
import type { ConfigDoc } from './types';

export class ConflictError extends Error {
  constructor() {
    super(
      'The configuration was changed elsewhere since this page loaded. The latest version has been loaded; please review and try again.'
    );
  }
}

export class NotLoadedError extends Error {
  constructor() {
    super('The configuration has not finished loading. Wait a moment and try again.');
  }
}

/** A plain copy of one loaded config version and the ETag naming it. */
export type ConfigSnapshot = { doc: ConfigDoc; etag: string };

const EXAMPLE_KEYS = new Set(['your-api-key-1', 'your-api-key-2', 'your-api-key-3']);

export function isExampleKey(key: string): boolean {
  return EXAMPLE_KEYS.has(key.trim());
}

class ConfigStore {
  data = $state<ConfigDoc | null>(null);
  etag = $state<string | null>(null);
  error = $state('');
  loading = $state(false);
  private inflight: Promise<void> | null = null;

  /** True once a config version (document and ETag) has been loaded. */
  get loaded(): boolean {
    return this.data !== null && this.etag !== null;
  }

  /** Snapshot of the loaded version, or null before the first successful load. */
  snapshot(): ConfigSnapshot | null {
    if (this.data === null || this.etag === null) return null;
    return { doc: $state.snapshot(this.data), etag: this.etag };
  }

  get exampleKeys(): string[] {
    return (this.data?.access?.['api-keys'] ?? []).filter(isExampleKey);
  }
  /** Client key labels by key id (access.api-key-labels). */
  get keyLabels(): Record<string, string> {
    return this.data?.access?.['api-key-labels'] ?? {};
  }
  get usageEnabled(): boolean {
    return this.data?.observability?.usage?.enabled !== false;
  }

  load(): Promise<void> {
    if (this.inflight) return this.inflight;
    this.loading = true;
    this.inflight = request<ConfigDoc>('GET', 'config')
      .then((r) => {
        this.data = r.data;
        this.etag = r.etag;
        this.error = '';
      })
      .catch((e) => {
        this.error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        this.loading = false;
        this.inflight = null;
      });
    return this.inflight;
  }

  ensure(): Promise<void> {
    return this.data ? Promise.resolve() : this.load();
  }

  /**
   * Sends a write guarded by If-Match. With a snapshot, its ETag is used, so a
   * newer server version fails with ConflictError instead of being replaced
   * by a value computed from older data. Without one, the latest loaded ETag
   * is used; before the first load the write is refused.
   */
  private async write(method: 'PATCH' | 'PUT' | 'DELETE', path: string, body: unknown, from?: ConfigSnapshot) {
    const etag = from ? from.etag : this.etag;
    if (!etag || (!from && this.data === null)) throw new NotLoadedError();
    try {
      // The response ETag is not stored on its own: data and etag only ever
      // change together (in load), so a snapshot can never pair the old
      // document with the new version's ETag.
      await request(method, path, { body, headers: { 'If-Match': etag } });
    } catch (e) {
      if (e instanceof ApiError && e.status === 412) {
        await this.reload();
        throw new ConflictError();
      }
      throw e;
    }
    await this.reload();
  }

  /** Loads a version newer than any read already in flight. */
  private async reload() {
    if (this.inflight) await this.inflight;
    await this.load();
  }

  /** Merge-patch the whole config (objects merge; lists and scalars replace). */
  patch(patch: ConfigDoc, from?: ConfigSnapshot) {
    return this.write('PATCH', 'config', patch, from);
  }

  /** Replace the value at a v8 path such as "access/api-keys". */
  put(path: string, value: unknown, from?: ConfigSnapshot) {
    return this.write('PUT', `config/${path}`, value, from);
  }

  remove(path: string, from?: ConfigSnapshot) {
    return this.write('DELETE', `config/${path}`, undefined, from);
  }
}

export const config = new ConfigStore();

/** Runs a config write and reports the result with toasts. Returns success. */
export async function saveConfig(action: () => Promise<void>, okText: string): Promise<boolean> {
  try {
    await action();
    toasts.ok(okText);
    return true;
  } catch (e) {
    toasts.error(e instanceof Error ? e.message : String(e));
    return false;
  }
}
