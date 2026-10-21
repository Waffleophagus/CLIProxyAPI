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
  /** Generation speed (output tokens per second) of qualifying streams; 0 without a sample. */
  output_tps: number;
  /** API-equivalent cost estimates (USD); zero when pricing is off. */
  cost_usd: number;
  api_spend_usd: number;
  subscription_value_usd: number;
  local_value_usd: number;
  unpriced_requests: number;
};

export type PricingStatus = {
  enabled: boolean;
  source: string;
  fetched_at?: string;
  models: number;
  error?: string;
};

export type SubscriptionFee = { account: string; provider?: string; monthly_usd: number; window_usd: number };

export type UsagePricingWindow = {
  catalog: PricingStatus;
  subscription_fees_usd: number;
  subscriptions: SubscriptionFee[];
};

export type PriceRates = { input: number; output: number; cache_read: number; cache_write: number; reasoning?: number };

export type Price = PriceRates & { tiers?: { size: number; rates: PriceRates }[]; modes?: Record<string, PriceRates> };

export type CatalogModel = { id: string; provider: string; provider_name: string; model: string; name: string; price: Price };

export type CostClass = 'api' | 'subscription' | 'local';

export type PriceVersion = { from?: string; priced_as?: string; source?: 'auto' | 'override'; price?: Price };

export type PricedModel = {
  provider: string;
  requested_model: string;
  upstream_model: string;
  response_model?: string;
  class: CostClass;
  priced_as?: string;
  source?: 'auto' | 'override';
  price?: Price;
  /** When the current price took effect (dated mappings). */
  from?: string;
  /** Every price over time, oldest first, when the price changed. */
  history?: PriceVersion[];
  requests: number;
  total_tokens: number;
  cost_usd: number;
};

/** A model configured on your own hardware that has no price. */
export type UnpricedLocalModel = { provider: string; group: string; alias: string; model: string };

export type UsagePriceModel = {
  model: string;
  /** RFC3339 time from which this price applies (price history). */
  from?: string;
  'price-as'?: string;
  input?: number;
  output?: number;
  'cache-read'?: number;
  'cache-write'?: number;
};

export type UsagePricingConfig = {
  enabled?: boolean;
  source?: string;
  models?: UsagePriceModel[];
  subscriptions?: { account: string; provider?: string; 'monthly-usd': number }[];
  'provider-classes'?: Record<string, CostClass>;
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
  store: { dropped: number; retention_days: number; rollup_retention_days: number };
  /** Present when cost estimates are enabled. */
  pricing?: UsagePricingWindow;
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
  /** Generation speed; absent when the request does not qualify (not streamed, buffered, failed). */
  output_tps?: number;
  cost_usd?: number;
  cost_class?: CostClass;
  priced_as?: string;
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
    usage?: {
      enabled?: boolean;
      path?: string;
      'retention-days'?: number;
      'rollup-retention-days'?: number;
      pricing?: UsagePricingConfig;
    };
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
