import type { SlugStatus } from './types';

export type SlugHealth = { tone: 'good' | 'warn' | 'bad' | 'neutral'; text: string };

export function slugHealth(s: SlugStatus): SlugHealth {
  if (s.last_error) return { tone: 'bad', text: s.resolved_model ? 'Endpoint down' : 'Unreachable' };
  if (s.mode === 'follow' && !s.resolved_model) return { tone: 'warn', text: 'Waiting for model' };
  if (s.last_success_at) return { tone: 'good', text: 'Endpoint up' };
  return { tone: 'neutral', text: 'Not checked' };
}

/** Base URL clients should use, derived from where this UI is served. */
export function clientBaseURL(): string {
  return `${location.origin}/v1`;
}
