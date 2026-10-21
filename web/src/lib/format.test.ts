import { describe, expect, it } from 'vitest';
import { perM, tps, usd } from './format';

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

describe('tps', () => {
  it('shows one decimal below 100 and none above', () => {
    expect(tps(27.84)).toBe('27.8 tok/s');
    expect(tps(1234.5)).toBe('1235 tok/s');
  });
  it('treats zero and missing as no sample', () => {
    expect(tps(0)).toBe('–');
    expect(tps(undefined)).toBe('–');
  });
});
