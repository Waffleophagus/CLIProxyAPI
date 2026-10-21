// Drives one sign-in dialog: start a session, poll its status, and cancel it.
// Every start is an attempt; abandon() (dialog closed or unmounted) bumps the
// attempt so late responses are ignored, and a session that is still pending
// on the server is cancelled, including one whose start response arrives
// after the dialog was closed.
import { errorMessage } from './api';
import type { OAuthStart, OAuthStatus } from './types';

export type OAuthFlowDeps = {
  start(): Promise<OAuthStart>;
  status(state: string, signal: AbortSignal): Promise<OAuthStatus>;
  cancel(state: string): Promise<unknown>;
  /** Calls fn every ms until the returned function is called. */
  every(fn: () => void, ms: number): () => void;
};

export type OAuthFlowEvents = {
  waiting(start: OAuthStart): void;
  done(): void;
  failed(message: string): void;
  pollError(message: string): void;
};

export const POLL_MS = 2000;

function isAbort(e: unknown): boolean {
  return (e as { name?: string } | null)?.name === 'AbortError';
}

export class OAuthFlow {
  private attempt = 0;
  private pending: string | null = null;
  private stopPolling: (() => void) | null = null;
  private controller: AbortController | null = null;

  constructor(
    private deps: OAuthFlowDeps,
    private events: OAuthFlowEvents
  ) {}

  /** State of the session that is still pending on the server, if any. */
  get session(): string | null {
    return this.pending;
  }

  async begin(): Promise<void> {
    this.abandon();
    const my = this.attempt;
    this.controller = new AbortController();
    let start: OAuthStart;
    try {
      // The start request is deliberately not aborted: the server may already
      // have created the session, and only the response tells us its state so
      // it can be cancelled.
      start = await this.deps.start();
    } catch (e) {
      if (my === this.attempt) this.events.failed(errorMessage(e));
      return;
    }
    if (my !== this.attempt) {
      this.cancelQuietly(start.state);
      return;
    }
    this.pending = start.state;
    this.events.waiting(start);
    this.stopPolling = this.deps.every(() => void this.check(), POLL_MS);
  }

  async check(): Promise<void> {
    const my = this.attempt;
    const state = this.pending;
    const signal = this.controller?.signal;
    if (!state || !signal) return;
    try {
      const s = await this.deps.status(state, signal);
      if (my !== this.attempt) return;
      if (s.status === 'ok') {
        this.finish();
        this.events.done();
      } else if (s.status === 'error') {
        this.finish();
        this.events.failed(s.error ?? 'Sign-in failed');
      }
    } catch (e) {
      if (my !== this.attempt || isAbort(e)) return;
      this.events.pollError(errorMessage(e));
    }
  }

  /** Invalidates the current attempt and cancels its pending session. */
  abandon(): void {
    this.attempt++;
    this.controller?.abort();
    this.controller = null;
    this.stopPolling?.();
    this.stopPolling = null;
    const state = this.pending;
    this.pending = null;
    if (state) this.cancelQuietly(state);
  }

  private finish() {
    // The server finished the session; nothing left to cancel.
    this.stopPolling?.();
    this.stopPolling = null;
    this.pending = null;
  }

  private cancelQuietly(state: string) {
    // The session expires on its own if this fails.
    this.deps.cancel(state).catch(() => {});
  }
}
