// App-wide health signals shown in the top status strip.
import { api } from './api';
import { auth } from './auth.svelte';
import type { TailscaleStatus } from './types';

class AppStatus {
  /** Set when a usage endpoint answered 409 (tracking disabled). */
  usageDisabled = $state(false);
  tailscaleError = $state('');

  async refreshTailscale(): Promise<TailscaleStatus | null> {
    try {
      const ts = await api.get<TailscaleStatus>('tailscale/status');
      auth.tailscale = ts;
      this.tailscaleError = '';
      return ts;
    } catch (e) {
      this.tailscaleError = e instanceof Error ? e.message : String(e);
      return null;
    }
  }
}

export const appStatus = new AppStatus();
