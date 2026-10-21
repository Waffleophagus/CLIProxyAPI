// Response shapes of the v8 management endpoints the UI uses.

export type Aggregate = {
  requests: number;
  successes: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  avg_latency_ms: number;
  max_latency_ms: number;
  avg_ttft_ms: number;
};

export type UsageGroup = Aggregate & { key: string; label: string };

export type UsageSummary = {
  from: string;
  to: string;
  timezone: string;
  group_by: string;
  totals: Aggregate;
  latency_p50_ms: number;
  latency_p95_ms: number;
  groups?: UsageGroup[];
  store: { dropped: number; retention_days: number };
};

export type UsagePoint = Aggregate & { t: string };

export type UsageSeries = { key: string; label: string; totals: Aggregate; points: UsagePoint[] };

export type UsageTimeseries = {
  from: string;
  to: string;
  timezone: string;
  bucket: 'hour' | 'day';
  group_by: string;
  buckets: string[];
  series: UsageSeries[];
};

export type UsageRow = {
  id: number;
  ts: string;
  request_id: string;
  trace_id?: string;
  provider: string;
  auth_id: string;
  auth_label: string;
  auth_type?: string;
  requested_model: string;
  upstream_model: string;
  response_model?: string;
  base_url?: string;
  client_key_id: string;
  client_key_masked: string;
  endpoint?: string;
  session_id?: string;
  stream: boolean;
  success: boolean;
  status: number;
  error?: string;
  latency_ms: number;
  ttft_ms: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  reasoning_effort?: string;
  service_tier?: string;
};

export type UsageRequests = { requests: UsageRow[]; next_before: number | null };

export type SlugStatus = {
  group: string;
  alias: string;
  base_url: string;
  mode: 'follow' | 'pinned';
  match?: string;
  resolved_model: string;
  poll_interval?: string;
  last_check_at?: string;
  last_success_at?: string;
  last_error?: string;
  reported_models: string[];
};

export type Cooldown = {
  scope: 'credential' | 'model';
  model_key?: string;
  reason: string;
  retry_at: string;
  remaining_seconds: number;
  http_status?: number;
};

export type Credential = {
  id: string;
  auth_index: string;
  name: string;
  type: string;
  provider: string;
  label: string;
  status: string;
  status_message: string;
  disabled: boolean;
  unavailable: boolean;
  runtime_only: boolean;
  source: string;
  email?: string;
  account?: string;
  account_type?: string;
  created_at?: string;
  updated_at?: string;
  last_refresh?: string;
  next_retry_after?: string;
  note?: string;
  priority?: number;
  success: number;
  failed: number;
  cooldowns: Cooldown[];
  siwc_bind_supported?: boolean;
  use_siwc?: boolean;
  siwc_bound?: boolean;
  siwc_expires_at?: string;
};

export type TailscaleStatus = {
  enabled: boolean;
  active: boolean;
  restart_required: boolean;
  state: string;
  hostname?: string;
  dns_name?: string;
  tailnet?: string;
  magic_dns_suffix?: string;
  magic_dns_enabled?: boolean;
  ips?: string[];
  https_enabled?: boolean;
  cert_domains?: string[];
  http_port?: number;
  urls?: string[];
  auth_url?: string;
  self_user?: string;
  tags?: string[];
  caller: {
    via_tailnet: boolean;
    login_name?: string;
    node_name?: string;
    tagged?: boolean;
    management_identity?: boolean;
  };
};

export type OAuthStart = {
  status: string;
  url: string;
  state: string;
  flow?: 'device' | 'siwc_bind';
  user_code?: string;
  expires_in?: number;
};

export type OAuthStatus = { status: 'ok' | 'wait' | 'error'; error?: string };

export type ErrorLogFile = { name: string; size: number; modified: number };

/** Loose view of the v8 JSON config; only the parts the UI edits are typed. */
export type ApiKeyEntry = { 'api-key'?: string; auth_index?: string; [k: string]: unknown };

export type CompatModel = {
  name?: string;
  alias?: string;
  follow?: boolean;
  match?: string;
  [k: string]: unknown;
};

export type CompatGroup = {
  name: string;
  'base-url': string;
  keys?: ApiKeyEntry[];
  models?: CompatModel[];
  'follow-interval'?: string;
  disabled?: boolean;
  auth_index?: string;
  [k: string]: unknown;
};

export type ProviderGroup = {
  name?: string;
  'base-url'?: string;
  keys?: ApiKeyEntry[];
  prefix?: string;
  [k: string]: unknown;
};

export type ConfigDoc = {
  server?: { host?: string; port?: number; [k: string]: unknown };
  management?: { 'allow-remote'?: boolean; 'disable-ui'?: boolean; [k: string]: unknown };
  access?: { 'api-keys'?: string[] };
  routing?: {
    strategy?: string;
    'session-affinity'?: boolean;
    retry?: { 'request-retry'?: number; 'max-retry-interval'?: number; 'max-retry-credentials'?: number };
    cooldown?: { 'disable-cooling'?: boolean; 'transient-error-cooldown-seconds'?: number };
    [k: string]: unknown;
  };
  'api-keys'?: {
    'openai-compatibility'?: CompatGroup[];
    codex?: ProviderGroup[];
    claude?: ProviderGroup[];
    xai?: ProviderGroup[];
    meta?: ProviderGroup[];
    [k: string]: unknown;
  };
  observability?: {
    logs?: { debug?: boolean; 'logging-to-file'?: boolean; 'request-log'?: boolean; [k: string]: unknown };
    usage?: { enabled?: boolean; path?: string; 'retention-days'?: number };
    [k: string]: unknown;
  };
  tailscale?: {
    enabled?: boolean;
    hostname?: string;
    https?: boolean;
    'http-port'?: number;
    ephemeral?: boolean;
    'management-users'?: string[];
    [k: string]: unknown;
  };
  [k: string]: unknown;
};
