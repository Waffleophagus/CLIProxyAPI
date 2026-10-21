import { ApiError, api } from './api';
import { browserTZ } from './format';
import { appStatus } from './status.svelte';
import type { UsageRequests, UsageSummary, UsageTimeseries } from './types';

export type UsageFilters = {
  from?: string;
  to?: string;
  model?: string;
  upstream_model?: string;
  provider?: string;
  credential?: string;
  api_key?: string;
  success?: string;
};

/** Thrown when usage tracking is disabled on the server (HTTP 409). */
export class UsageDisabledError extends Error {}

async function wrap<T>(p: Promise<T>): Promise<T> {
  try {
    const v = await p;
    appStatus.usageDisabled = false;
    return v;
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      appStatus.usageDisabled = true;
      throw new UsageDisabledError(e.message);
    }
    throw e;
  }
}

export function usageSummary(filters: UsageFilters, groupBy = '') {
  return wrap(api.get<UsageSummary>('observability/usage/summary', { query: { ...filters, group_by: groupBy, tz: browserTZ } }));
}

export function usageTimeseries(filters: UsageFilters, bucket: 'hour' | 'day', groupBy = '', top = 7) {
  return wrap(
    api.get<UsageTimeseries>('observability/usage/timeseries', {
      query: { ...filters, bucket, group_by: groupBy, top: groupBy ? top : undefined, tz: browserTZ }
    })
  );
}

export function usageRequests(filters: UsageFilters, limit = 50, before?: number | null) {
  return wrap(
    api.get<UsageRequests>('observability/usage/requests', { query: { ...filters, limit, before: before ?? undefined, tz: browserTZ } })
  );
}

export type RelativeRange = '24h' | '7d' | '30d' | '90d';
export type UsageRange = RelativeRange | 'custom';
export type UsageWindow = { from: string; to: string; bucket: 'hour' | 'day' };

/**
 * Window ending at now, aligned to bucket boundaries in the browser zone.
 * Relative windows go stale as time passes: compute one per load, never cache.
 */
export function windowFor(range: RelativeRange, now: Date = new Date()): UsageWindow {
  if (range === '24h') {
    const from = new Date(now);
    from.setMinutes(0, 0, 0);
    from.setHours(from.getHours() - 23);
    return { from: from.toISOString(), to: now.toISOString(), bucket: 'hour' };
  }
  const days = range === '7d' ? 7 : range === '30d' ? 30 : 90;
  const from = new Date(now);
  from.setHours(0, 0, 0, 0);
  from.setDate(from.getDate() - (days - 1));
  return { from: from.toISOString(), to: now.toISOString(), bucket: 'day' };
}

/**
 * Resolves the window for one load. Relative ranges end at now; a custom
 * range is fixed by its inputs (datetime-local strings). An empty custom end
 * means "until now", an empty start means 7 days before the end.
 */
export function resolveWindow(range: UsageRange, customFrom: string, customTo: string, now: Date = new Date()): UsageWindow {
  if (range !== 'custom') return windowFor(range, now);
  const to = customTo ? new Date(customTo) : now;
  const from = customFrom ? new Date(customFrom) : new Date(to.getTime() - 7 * 86400_000);
  const span = to.getTime() - from.getTime();
  return { from: from.toISOString(), to: to.toISOString(), bucket: span <= 3 * 86400_000 ? 'hour' : 'day' };
}
