import { describe, expect, it } from 'vitest';
import { dirtyFields, rebaseDraft, revertFields } from './drafts';

type D = { retry: number; strategy: string; retention: number; debug: boolean };

describe('settings drafts', () => {
  const base: D = { retry: 1, strategy: 'round-robin', retention: 30, debug: false };

  it('saving one card keeps unsaved edits of other cards', () => {
    // User edits routing.retry and usage.retention, then saves routing only.
    const draft: D = { ...base, retry: 2, retention: 45 };
    const server: D = { ...base, retry: 2 };
    const next = rebaseDraft(draft, base, server, ['retry', 'strategy']);
    expect(next).toEqual({ ...server, retention: 45 });
    expect(dirtyFields(next, server)).toEqual(['retention']);
  });

  it('clean fields follow server changes made elsewhere', () => {
    const draft: D = { ...base, retention: 45 };
    const server: D = { ...base, debug: true, strategy: 'fill-first' };
    expect(rebaseDraft(draft, base, server)).toEqual({ ...server, retention: 45 });
  });

  it('a saved field takes the server value even when normalized', () => {
    const draft: D = { ...base, retention: 0 };
    const server: D = { ...base, retention: 90 };
    expect(rebaseDraft(draft, base, server, ['retention']).retention).toBe(90);
  });

  it('reverts only the given fields', () => {
    const draft: D = { ...base, retry: 5, retention: 45 };
    expect(revertFields(draft, base, ['retry'])).toEqual({ ...base, retention: 45 });
  });
});
