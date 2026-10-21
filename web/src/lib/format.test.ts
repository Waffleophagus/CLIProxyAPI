import { describe, expect, it } from 'vitest';
import { perM, usd } from './format';

describe('usd', () => {
  it('formats cents, sub-cent and large amounts', () => {
    expect(usd(12.345)).toContain('12.35');
    expect(usd(0.0042)).toContain('0.0042');
    expect(usd(25_000)).toMatch(/25(\.0)?K/);
    expect(usd(undefined)).toBe('–');
  });
});

describe('perM', () => {
  it('trims floating noise', () => {
    expect(perM(0.30000000000000004)).toBe('$0.3');
    expect(perM(6.25)).toBe('$6.25');
  });
});
