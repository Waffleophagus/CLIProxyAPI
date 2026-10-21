// Display formatting. Times render in the browser's time zone.

export const browserTZ = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';

const compactFmt = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const intFmt = new Intl.NumberFormat();

export function compact(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '–';
  if (Math.abs(n) < 1000) return intFmt.format(Math.round(n));
  return compactFmt.format(n);
}

export function int(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '–';
  return intFmt.format(Math.round(n));
}

export function pct(part: number, total: number): string {
  if (!total) return '–';
  const v = (part / total) * 100;
  if (v === 100 || v === 0) return `${v}%`;
  return `${v >= 99.95 ? 99.9 : v.toFixed(1)}%`;
}

export function ms(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v) || v <= 0) return '–';
  if (v < 1000) return `${Math.round(v)} ms`;
  if (v < 60_000) return `${(v / 1000).toFixed(v < 10_000 ? 2 : 1)} s`;
  return `${(v / 60_000).toFixed(1)} min`;
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const dateTimeFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' });
const shortFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });

function toDate(v: string | number | Date): Date {
  return v instanceof Date ? v : new Date(v);
}

export function dateTime(v: string | number | Date | undefined | null): string {
  if (!v) return '–';
  const d = toDate(v);
  return Number.isNaN(d.getTime()) ? String(v) : dateTimeFmt.format(d);
}

export function shortDateTime(v: string | number | Date | undefined | null): string {
  if (!v) return '–';
  const d = toDate(v);
  if (Number.isNaN(d.getTime())) return String(v);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return timeFmt.format(d);
  return shortFmt.format(d);
}

export function relative(v: string | number | Date | undefined | null): string {
  if (!v) return 'never';
  const d = toDate(v);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (Number.isNaN(diff)) return '–';
  const abs = Math.abs(diff);
  const future = diff < 0;
  let s: string;
  if (abs < 10) return future ? 'in a moment' : 'just now';
  if (abs < 60) s = `${Math.round(abs)} s`;
  else if (abs < 3600) s = `${Math.round(abs / 60)} min`;
  else if (abs < 86400) s = `${Math.round(abs / 3600)} h`;
  else s = `${Math.round(abs / 86400)} d`;
  return future ? `in ${s}` : `${s} ago`;
}

/** Masks a secret like the server does: short prefix + last four characters. */
export function mask(secret: string): string {
  const s = secret.trim();
  if (!s) return '';
  if (s.length < 12) return '…' + s.slice(-2);
  return s.slice(0, 3) + '…' + s.slice(-4);
}

export function providerName(p: string): string {
  const key = p.toLowerCase();
  const names: Record<string, string> = {
    claude: 'Claude',
    anthropic: 'Claude',
    codex: 'Codex',
    xai: 'xAI',
    kimi: 'Kimi',
    meta: 'Meta',
    devin: 'Devin',
    'openai-compatibility': 'OpenAI-compatible'
  };
  if (names[key]) return names[key];
  if (key.startsWith('openai-compatible-')) return `${p.slice('openai-compatible-'.length)} (OpenAI-compatible)`;
  return p;
}
