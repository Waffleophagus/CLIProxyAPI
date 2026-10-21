import { afterEach, describe, expect, it, vi } from 'vitest';

function blockStorage() {
  const thrower = () => {
    throw new DOMException('The operation is insecure.', 'SecurityError');
  };
  Object.defineProperty(globalThis, 'localStorage', { get: thrower, configurable: true });
  Object.defineProperty(globalThis, 'sessionStorage', { get: thrower, configurable: true });
}

function fakeDocument() {
  const attrs = new Map<string, string>();
  vi.stubGlobal('document', {
    documentElement: {
      setAttribute: (k: string, v: string) => attrs.set(k, v),
      removeAttribute: (k: string) => attrs.delete(k)
    }
  });
  return attrs;
}

afterEach(() => {
  delete (globalThis as Record<string, unknown>).localStorage;
  delete (globalThis as Record<string, unknown>).sessionStorage;
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe('storage blocked by the browser', () => {
  it('safe helpers fall back to memory', async () => {
    blockStorage();
    const { storageGet, storageRemove, storageSet } = await import('./storage');
    expect(storageGet('local', 'k')).toBeNull();
    storageSet('session', 'k', 'v');
    expect(storageGet('session', 'k')).toBe('v');
    storageRemove('session', 'k');
    expect(storageGet('session', 'k')).toBeNull();
  });

  it('theme init and changes do not throw', async () => {
    blockStorage();
    const attrs = fakeDocument();
    const { theme } = await import('./theme.svelte');
    expect(() => theme.init()).not.toThrow();
    expect(() => theme.set('dark')).not.toThrow();
    expect(attrs.get('data-theme')).toBe('dark');
  });

  it('uses real storage when available', async () => {
    const data = new Map<string, string>();
    const fake = {
      getItem: (k: string) => data.get(k) ?? null,
      setItem: (k: string, v: string) => void data.set(k, v),
      removeItem: (k: string) => void data.delete(k)
    };
    Object.defineProperty(globalThis, 'localStorage', { value: fake, configurable: true });
    const { storageGet, storageSet } = await import('./storage');
    storageSet('local', 'cliproxy.theme', 'light');
    expect(data.get('cliproxy.theme')).toBe('light');
    expect(storageGet('local', 'cliproxy.theme')).toBe('light');
  });
});
