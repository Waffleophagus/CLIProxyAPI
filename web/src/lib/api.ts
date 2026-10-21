// Thin client for the v8 management API. All calls are same-origin and
// relative so the UI works behind any host name (LAN, tailnet, reverse proxy).

export const MGMT = '/v8/management';

export class ApiError extends Error {
  status: number;
  code: string;
  body: unknown;
  constructor(status: number, code: string, message: string, body: unknown) {
    super(message);
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

type AuthHooks = {
  key: () => string;
  onUnauthorized: (err: ApiError) => void;
};

let hooks: AuthHooks = { key: () => '', onUnauthorized: () => {} };

export function setAuthHooks(h: AuthHooks) {
  hooks = h;
}

export type RequestOptions = {
  query?: Record<string, string | number | boolean | undefined | null>;
  body?: unknown;
  rawBody?: string;
  contentType?: string;
  headers?: Record<string, string>;
  /** Do not trigger the global sign-out flow on 401/403. */
  noAuthRedirect?: boolean;
  /** Send without the management key (tailnet identity probe). */
  anonymous?: boolean;
  signal?: AbortSignal;
};

export type ApiResponse<T> = { data: T; etag: string | null; status: number };

function buildURL(path: string, query?: RequestOptions['query']): string {
  const url = path.startsWith('/') ? path : `${MGMT}/${path}`;
  if (!query) return url;
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    params.set(k, String(v));
  }
  const qs = params.toString();
  return qs ? `${url}?${qs}` : url;
}

function messageFor(status: number, body: unknown, text: string): { code: string; message: string } {
  if (body && typeof body === 'object') {
    const b = body as Record<string, unknown>;
    const code = typeof b.error === 'string' ? b.error : '';
    const msg = typeof b.message === 'string' ? b.message : '';
    if (code || msg) return { code, message: msg ? (code && code !== msg ? `${msg}` : msg) : code };
  }
  if (status === 404 && !text) return { code: 'management_unavailable', message: 'Management API is not available' };
  return { code: `http_${status}`, message: text.trim().slice(0, 300) || `Request failed (HTTP ${status})` };
}

export async function request<T = unknown>(method: string, path: string, opts: RequestOptions = {}): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = { Accept: 'application/json', ...(opts.headers ?? {}) };
  const key = opts.anonymous ? '' : hooks.key();
  if (key) headers.Authorization = `Bearer ${key}`;
  let body: string | undefined;
  if (opts.rawBody !== undefined) {
    body = opts.rawBody;
    headers['Content-Type'] = opts.contentType ?? 'text/plain';
  } else if (opts.body !== undefined) {
    body = JSON.stringify(opts.body);
    headers['Content-Type'] = 'application/json';
  }
  let res: Response;
  try {
    res = await fetch(buildURL(path, opts.query), { method, headers, body, signal: opts.signal, credentials: 'same-origin' });
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e;
    throw new ApiError(0, 'network', 'Cannot reach the proxy. Is it running?', null);
  }
  const text = await res.text();
  const ctype = res.headers.get('Content-Type') ?? '';
  let data: unknown = text;
  if (ctype.includes('json') && text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const { code, message } = messageFor(res.status, data, ctype.includes('json') ? '' : text);
    const err = new ApiError(res.status, code, message, data);
    if (
      !opts.noAuthRedirect &&
      (res.status === 401 || (res.status === 403 && isAuthForbidden(code)) || code === 'management_unavailable')
    ) {
      hooks.onUnauthorized(err);
    }
    throw err;
  }
  return { data: data as T, etag: res.headers.get('ETag'), status: res.status };
}

/** 403 responses from the management auth layer (not from business logic). */
export function isAuthForbidden(code: string): boolean {
  return code === 'remote management disabled' || code === 'remote management key not set' || code.startsWith('IP banned');
}

export const api = {
  get: <T = unknown>(path: string, opts?: RequestOptions) => request<T>('GET', path, opts).then((r) => r.data),
  getFull: <T = unknown>(path: string, opts?: RequestOptions) => request<T>('GET', path, opts),
  post: <T = unknown>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('POST', path, { ...opts, body }).then((r) => r.data),
  patch: <T = unknown>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('PATCH', path, { ...opts, body }).then((r) => r.data),
  put: <T = unknown>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('PUT', path, { ...opts, body }).then((r) => r.data),
  del: <T = unknown>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, opts).then((r) => r.data)
};

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message || e.code || `HTTP ${e.status}`;
  if (e instanceof Error) return e.message;
  return String(e);
}
