import { relative, shortDateTime } from './format';
import type { Credential } from './types';

export type HealthCategory = 'active' | 'cooling' | 'disabled' | 'relogin' | 'error';

export type Health = {
  category: HealthCategory;
  tone: 'good' | 'warn' | 'bad' | 'neutral';
  text: string;
  detail: string;
};

const RELOGIN_REASONS = new Set(['unauthorized', 'invalid_grant']);

/** Turns the credential's scheduler state into one plain-language status. */
export function credentialHealth(c: Credential): Health {
  if (c.disabled || c.status === 'disabled') {
    return { category: 'disabled', tone: 'neutral', text: 'Disabled', detail: 'Not used for requests until enabled.' };
  }
  const msg = (c.status_message ?? '').trim();
  const cooldowns = c.cooldowns ?? [];
  const relogin =
    cooldowns.some((cd) => RELOGIN_REASONS.has(cd.reason)) || /expired|unauthori[sz]ed|invalid_grant|re-?login|revoked/i.test(msg);
  if (relogin && c.status === 'error') {
    return { category: 'relogin', tone: 'bad', text: 'Needs sign-in', detail: msg || 'The provider rejected this account. Sign in again.' };
  }
  const credentialWide = cooldowns.find((cd) => cd.scope === 'credential');
  if (credentialWide || (c.unavailable && c.next_retry_after)) {
    const until = credentialWide?.retry_at ?? c.next_retry_after;
    return {
      category: 'cooling',
      tone: 'warn',
      text: `Cooling down`,
      detail: `Resting ${relative(until)} (until ${shortDateTime(until)})${credentialWide?.reason && credentialWide.reason !== 'unknown' ? ` · ${reasonText(credentialWide.reason)}` : ''}`
    };
  }
  if (c.status === 'error' || c.unavailable) {
    return { category: 'error', tone: 'bad', text: 'Error', detail: msg || 'The last request with this account failed.' };
  }
  if (cooldowns.length > 0) {
    const models = cooldowns.map((cd) => cd.model_key).filter(Boolean);
    return {
      category: 'active',
      tone: 'warn',
      text: 'Partly cooling',
      detail: `${models.length} model${models.length === 1 ? '' : 's'} resting: ${models.slice(0, 3).join(', ')}${models.length > 3 ? '…' : ''}`
    };
  }
  return { category: 'active', tone: 'good', text: 'Active', detail: '' };
}

export function reasonText(reason: string): string {
  const map: Record<string, string> = {
    quota: 'usage limit reached',
    credential_quota: 'account usage limit reached',
    unauthorized: 'unauthorized',
    payment_required: 'payment required / forbidden',
    invalid_grant: 'sign-in expired',
    cloudflare_challenge: 'blocked by Cloudflare challenge',
    model_not_supported: 'model not supported',
    rate_limited: 'rate limited'
  };
  return map[reason] ?? reason.replace(/_/g, ' ');
}

export function accountTitle(c: Credential): string {
  return c.email || c.label || c.account || c.name;
}
