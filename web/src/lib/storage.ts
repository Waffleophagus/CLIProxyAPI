// Web Storage can be missing or throw (SecurityError when the browser blocks
// site data, QuotaExceededError in some private modes). Even reading the
// window.localStorage property can throw. These helpers never throw: when the
// real storage is unusable they fall back to an in-memory map for this tab.

export type StorageKind = 'local' | 'session';

const memory: Record<StorageKind, Map<string, string>> = { local: new Map(), session: new Map() };

function backend(kind: StorageKind): Storage | null {
  try {
    const s = kind === 'local' ? globalThis.localStorage : globalThis.sessionStorage;
    return s ?? null;
  } catch {
    return null;
  }
}

export function storageGet(kind: StorageKind, key: string): string | null {
  const s = backend(kind);
  if (s) {
    try {
      const v = s.getItem(key);
      if (v !== null) return v;
    } catch {
      // Fall back to memory.
    }
  }
  return memory[kind].get(key) ?? null;
}

export function storageSet(kind: StorageKind, key: string, value: string): void {
  memory[kind].set(key, value);
  const s = backend(kind);
  if (!s) return;
  try {
    s.setItem(key, value);
  } catch {
    // Kept in memory only.
  }
}

export function storageRemove(kind: StorageKind, key: string): void {
  memory[kind].delete(key);
  const s = backend(kind);
  if (!s) return;
  try {
    s.removeItem(key);
  } catch {
    // Nothing persisted to remove.
  }
}
