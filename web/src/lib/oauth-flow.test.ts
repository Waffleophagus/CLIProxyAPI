import { describe, expect, it, vi } from 'vitest';
import { OAuthFlow } from './oauth-flow';
import type { OAuthStart, OAuthStatus } from './types';

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup() {
  const starts: ReturnType<typeof deferred<OAuthStart>>[] = [];
  const statuses: { state: string; signal: AbortSignal; d: ReturnType<typeof deferred<OAuthStatus>> }[] = [];
  const timers = { started: 0, stopped: 0 };
  const deps = {
    start: vi.fn(() => {
      const d = deferred<OAuthStart>();
      starts.push(d);
      return d.promise;
    }),
    status: vi.fn((state: string, signal: AbortSignal) => {
      const d = deferred<OAuthStatus>();
      statuses.push({ state, signal, d });
      return d.promise;
    }),
    cancel: vi.fn((_state: string) => Promise.resolve({})),
    every: vi.fn((_fn: () => void, _ms: number) => {
      timers.started++;
      return () => timers.stopped++;
    })
  };
  const events = { waiting: vi.fn(), done: vi.fn(), failed: vi.fn(), pollError: vi.fn() };
  return { flow: new OAuthFlow(deps, events), deps, events, starts, statuses, timers };
}

const start = (state: string) => ({ url: 'https://example.test/auth', state }) as OAuthStart;
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('OAuth dialog flow', () => {
  it('cancels a session whose start response arrives after the dialog closed', async () => {
    const t = setup();
    const begun = t.flow.begin();
    t.flow.abandon(); // dialog closed while the start request is in flight
    t.starts[0].resolve(start('late-state'));
    await begun;
    expect(t.deps.cancel).toHaveBeenCalledWith('late-state');
    expect(t.events.waiting).not.toHaveBeenCalled();
    expect(t.deps.every).not.toHaveBeenCalled();
    expect(t.flow.session).toBeNull();
  });

  it('closing while waiting stops polling, aborts the status request and cancels the session', async () => {
    const t = setup();
    const begun = t.flow.begin();
    t.starts[0].resolve(start('s1'));
    await begun;
    expect(t.events.waiting).toHaveBeenCalledTimes(1);
    const check = t.flow.check();
    t.flow.abandon();
    expect(t.statuses[0].signal.aborted).toBe(true);
    expect(t.timers.stopped).toBe(1);
    expect(t.deps.cancel).toHaveBeenCalledWith('s1');
    t.statuses[0].d.resolve({ status: 'ok' });
    await check;
    expect(t.events.done).not.toHaveBeenCalled();
  });

  it('a finished session is not cancelled on close', async () => {
    const t = setup();
    const begun = t.flow.begin();
    t.starts[0].resolve(start('s2'));
    await begun;
    const check = t.flow.check();
    t.statuses[0].d.resolve({ status: 'ok' });
    await check;
    expect(t.events.done).toHaveBeenCalledTimes(1);
    t.flow.abandon();
    expect(t.deps.cancel).not.toHaveBeenCalled();
  });

  it('a start failure after close is not reported', async () => {
    const t = setup();
    const begun = t.flow.begin();
    t.flow.abandon();
    t.starts[0].reject(new Error('boom'));
    await begun;
    await flush();
    expect(t.events.failed).not.toHaveBeenCalled();
  });

  it('retrying cancels the previous pending session', async () => {
    const t = setup();
    const first = t.flow.begin();
    t.starts[0].resolve(start('old'));
    await first;
    const second = t.flow.begin();
    expect(t.deps.cancel).toHaveBeenCalledWith('old');
    t.starts[1].resolve(start('new'));
    await second;
    expect(t.flow.session).toBe('new');
  });
});
