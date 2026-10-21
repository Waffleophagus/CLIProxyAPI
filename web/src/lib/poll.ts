/**
 * Calls fn every intervalMs while the page is visible; returns a stop function.
 * A returned promise is not awaited, so fn must handle its own errors.
 */
export function poll(fn: () => void | Promise<void>, intervalMs: number): () => void {
  let timer: ReturnType<typeof setInterval> | null = null;
  const run = () => void fn();
  const start = () => {
    if (!timer) timer = setInterval(run, intervalMs);
  };
  const stop = () => {
    if (timer) clearInterval(timer);
    timer = null;
  };
  const onVis = () => {
    if (document.hidden) stop();
    else {
      run();
      start();
    }
  };
  document.addEventListener('visibilitychange', onVis);
  if (!document.hidden) start();
  return () => {
    stop();
    document.removeEventListener('visibilitychange', onVis);
  };
}
