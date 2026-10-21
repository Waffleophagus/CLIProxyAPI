// Minimal history router for the /ui/ base path.

export const BASE = '/ui';

export const routes = [
  { path: '/', label: 'Overview' },
  { path: '/slugs', label: 'Slugs' },
  { path: '/accounts', label: 'Accounts' },
  { path: '/keys', label: 'Client keys' },
  { path: '/usage', label: 'Usage' },
  { path: '/logs', label: 'Logs' },
  { path: '/settings', label: 'Settings' }
] as const;

function current(): { path: string; query: URLSearchParams } {
  let p = location.pathname;
  if (p.startsWith(BASE)) p = p.slice(BASE.length);
  if (!p.startsWith('/')) p = '/' + p;
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1);
  return { path: p, query: new URLSearchParams(location.search) };
}

/** Returns a message when leaving the current page would lose work. */
export type LeaveGuard = () => string | null;

class Router {
  path = $state('/');
  query = $state(new URLSearchParams());
  private guards = new Set<LeaveGuard>();

  /** Registers a guard for in-app navigation and page unload; returns the remover. */
  guard(fn: LeaveGuard): () => void {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (fn()) e.preventDefault();
    };
    this.guards.add(fn);
    window.addEventListener('beforeunload', onUnload);
    return () => {
      this.guards.delete(fn);
      window.removeEventListener('beforeunload', onUnload);
    };
  }

  /** Asks the user before leaving a page with unsaved work. */
  mayLeave(): boolean {
    for (const g of this.guards) {
      const msg = g();
      if (msg && !window.confirm(msg)) return false;
    }
    return true;
  }

  init() {
    this.sync();
    window.addEventListener('popstate', () => this.sync());
  }
  sync() {
    const c = current();
    this.path = c.path;
    this.query = c.query;
  }
  href(path: string, query?: Record<string, string>) {
    const qs = query ? new URLSearchParams(query).toString() : '';
    return BASE + (path === '/' ? '/' : path) + (qs ? `?${qs}` : '');
  }
  go(path: string, query?: Record<string, string>, replace = false) {
    if (!replace && !this.mayLeave()) return;
    const url = this.href(path, query);
    if (replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
    this.sync();
    if (!replace) window.scrollTo(0, 0);
  }
  /** Replace the query string of the current page without a new history entry. */
  setQuery(query: Record<string, string | undefined>) {
    const params = new URLSearchParams(Object.entries(query).filter((e): e is [string, string] => Boolean(e[1])));
    const qs = params.toString();
    history.replaceState(null, '', BASE + (this.path === '/' ? '/' : this.path) + (qs ? `?${qs}` : ''));
    this.query = params;
  }
}

export const router = new Router();

/** Click handler for in-app links: keeps modifier-clicks working. */
export function link(node: HTMLAnchorElement) {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const href = node.getAttribute('href');
    if (!href || !href.startsWith(BASE) || node.target) return;
    e.preventDefault();
    if (!router.mayLeave()) return;
    history.pushState(null, '', href);
    router.sync();
    window.scrollTo(0, 0);
  };
  node.addEventListener('click', onClick);
  return { destroy: () => node.removeEventListener('click', onClick) };
}
