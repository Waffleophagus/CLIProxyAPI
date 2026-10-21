import { describe, expect, it, vi } from 'vitest';

vi.mock('./status.svelte', () => ({ appStatus: { usageDisabled: false } }));
vi.mock('./format', () => ({ browserTZ: 'UTC' }));

const { resolveWindow, windowFor } = await import('./usage');

describe('usage windows', () => {
  it('relative ranges end at the time of each load', () => {
    const t1 = new Date('2026-10-20T10:00:00Z');
    const t2 = new Date('2026-10-20T10:05:00Z');
    for (const range of ['24h', '7d', '30d', '90d'] as const) {
      const a = resolveWindow(range, '', '', t1);
      const b = resolveWindow(range, '', '', t2);
      expect(a.to).toBe(t1.toISOString());
      expect(b.to).toBe(t2.toISOString());
    }
    expect(windowFor('24h', t2).bucket).toBe('hour');
    expect(windowFor('7d', t2).bucket).toBe('day');
  });

  it('custom ranges stay fixed across refreshes', () => {
    const a = resolveWindow('custom', '2026-10-01T00:00', '2026-10-02T00:00', new Date('2026-10-20T10:00:00Z'));
    const b = resolveWindow('custom', '2026-10-01T00:00', '2026-10-02T00:00', new Date('2026-10-21T10:00:00Z'));
    expect(b).toEqual(a);
    expect(a.bucket).toBe('hour');
  });

  it('a custom range without an end runs until now', () => {
    const now = new Date('2026-10-20T10:00:00Z');
    const w = resolveWindow('custom', '2026-10-01T00:00', '', now);
    expect(w.to).toBe(now.toISOString());
    expect(w.bucket).toBe('day');
  });
});
