import { ApiError, request, setAuthHooks } from './api';
import { storageGet, storageRemove, storageSet } from './storage';
import type { TailscaleStatus } from './types';

const KEY = 'cliproxy.mgmt-key';

export type AuthPhase = 'checking' | 'login' | 'ready' | 'disabled';

function readStored(): { key: string; remember: boolean } {
  const session = storageGet('session', KEY);
  if (session) return { key: session, remember: false };
  const local = storageGet('local', KEY);
  if (local) return { key: local, remember: true };
  return { key: '', remember: false };
}

/** Explains a failed management call in plain words for the sign-in screen. */
export function explainAuthError(e: ApiError): string {
  if (e.status === 0) return e.message;
  if (e.code === 'remote management disabled') {
    return 'This proxy only accepts management requests from the machine it runs on. To manage it from here, set management.allow-remote: true (with a strong management.secret-key) or set the MANAGEMENT_PASSWORD environment variable, then reload this page.';
  }
  if (e.code === 'remote management key not set') {
    return 'No management key is configured. Set management.secret-key in config.yaml or the MANAGEMENT_PASSWORD environment variable.';
  }
  if (e.code.startsWith('IP banned')) return e.code + '.';
  if (e.code === 'invalid management key') return 'That management key was not accepted.';
  if (e.code === 'missing management key') return '';
  return e.message;
}

class Auth {
  phase = $state<AuthPhase>('checking');
  key = $state('');
  remember = $state(false);
  /** Tailnet login name when signed in by Tailscale identity. */
  identity = $state('');
  message = $state('');
  tailscale = $state<TailscaleStatus | null>(null);

  constructor() {
    setAuthHooks({
      key: () => this.key,
      onUnauthorized: (err) => this.handleUnauthorized(err)
    });
  }

  async init() {
    const stored = readStored();
    this.key = stored.key;
    this.remember = stored.remember;
    this.phase = 'checking';
    try {
      // Without a key this only succeeds for tailnet identity callers. A
      // missing key does not count toward the server's brute-force ban.
      const status = await this.probe(stored.key ? undefined : true);
      if (!stored.key && !status.caller?.management_identity) {
        this.phase = 'login';
        return;
      }
      this.accept(status);
    } catch (e) {
      this.fail(e, Boolean(stored.key));
    }
  }

  private probe(anonymous?: boolean) {
    return request<TailscaleStatus>('GET', 'tailscale/status', { noAuthRedirect: true, anonymous }).then((r) => r.data);
  }

  private accept(status: TailscaleStatus) {
    this.tailscale = status;
    this.identity = !this.key && status.caller?.management_identity ? (status.caller.login_name ?? 'tailnet user') : '';
    this.message = '';
    this.phase = 'ready';
  }

  private fail(e: unknown, hadKey: boolean) {
    if (e instanceof ApiError) {
      if (e.code === 'management_unavailable') {
        this.phase = 'disabled';
        return;
      }
      if (e.status === 401 || e.status === 403) {
        if (e.status === 401 && hadKey) this.clearKey();
        const why = explainAuthError(e);
        this.message =
          hadKey && e.code === 'invalid management key' ? 'Your saved management key is no longer valid. Enter the current key.' : why;
        this.phase = 'login';
        return;
      }
      this.message = e.message;
    } else {
      this.message = String(e);
    }
    this.phase = 'login';
  }

  async signIn(key: string, remember: boolean): Promise<boolean> {
    const previous = this.key;
    this.key = key.trim();
    try {
      const status = await this.probe();
      this.remember = remember;
      this.persist();
      this.accept(status);
      return true;
    } catch (e) {
      this.key = previous;
      if (e instanceof ApiError && e.code === 'management_unavailable') {
        this.phase = 'disabled';
        return false;
      }
      this.message = e instanceof ApiError ? explainAuthError(e) || e.message : String(e);
      return false;
    }
  }

  signOut() {
    this.clearKey();
    this.identity = '';
    this.message = '';
    this.phase = 'login';
  }

  private persist() {
    // Storage failures are absorbed by the helpers; the key then stays in
    // memory for this tab.
    storageRemove('session', KEY);
    storageRemove('local', KEY);
    if (!this.key) return;
    storageSet(this.remember ? 'local' : 'session', KEY, this.key);
  }

  private clearKey() {
    this.key = '';
    this.persist();
  }

  private handleUnauthorized(err: ApiError) {
    if (this.phase !== 'ready') return;
    if (err.code === 'management_unavailable') {
      this.phase = 'disabled';
      return;
    }
    if (err.status === 401) this.clearKey();
    this.message = explainAuthError(err) || 'Your session ended. Enter the management key to continue.';
    if (err.status === 401 && !this.message) this.message = 'Please sign in again.';
    this.identity = '';
    this.phase = 'login';
  }
}

export const auth = new Auth();
