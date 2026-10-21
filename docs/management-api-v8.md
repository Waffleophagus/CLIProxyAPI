# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.yaml](../config.example.yaml); operational endpoints are grouped
by function. It is the only management API; the legacy `/v0/management` API has
been removed.

## Access

The v8 endpoints require the management key (`management.secret-key` or the
`MANAGEMENT_PASSWORD` environment variable). With `management.allow-remote: false`
only localhost (`127.0.0.1`/`::1`) clients are accepted, unless
`MANAGEMENT_PASSWORD` is set: the environment variable always enables remote
access, regardless of `allow-remote`, and either key is then accepted remotely.
Send `Authorization: Bearer <management-key>` or `X-Management-Key: <management-key>`.
OAuth callbacks validate a pending login state and do not require a management-key
header.

Requests that arrive over the embedded Tailscale node (see `tailscale` in
[config.example.yaml](../config.example.yaml)) from a user listed in
`tailscale.management-users` are authorized by their tailnet identity instead:
the proxy looks up the connection's peer with Tailscale WhoIs, matches the
owner's login name case-insensitively, and skips the management key and
`allow-remote` checks. Tagged devices have no user identity and never qualify.
When `tailscale.management-users` is non-empty the management API is available
even without a management key, but only to those tailnet users; every other
caller, including all LAN requests, gets `404` until a key is configured.

Tailnet identity is ambient authority (a browser on an authorized device would
attach it to any page's requests), so it only applies to requests that are
same-origin or come from a non-browser client, addressed to the node itself:

- No `Origin` header, and `Sec-Fetch-Site` absent, `same-origin`, or `none`
  (curl, scripts, and same-origin page loads); or an `Origin` that exactly
  matches the request's own scheme and `Host` (`https` on the tailnet HTTPS
  listener, `http` on `http-port`; default ports may be omitted).
- `Host` is the node's MagicDNS name (full or short, any port) or an IP
  literal. Other names, which could be an attacker's domain rebound to the
  node's address, are refused.

Requests failing these checks (cross-origin pages, `Sec-Fetch-Site: cross-site`
or `same-site`, `Origin: null`) fall back to the management key: `401` when a
key is configured (a wrong key counts toward the IP ban; a request with no key
at all does not), otherwise `404`. The management key
itself is accepted regardless of `Origin`.

### Cross-origin requests (CORS)

The management API is same-origin only. Responses under `/v8/management` carry
no `Access-Control-*` headers and preflight `OPTIONS` requests are not answered
(`404`), so browsers block cross-origin pages from reading responses or sending
non-simple requests. Use the built-in UI served by the proxy or a non-browser
client. Inference routes (`/v1`, `/v1beta`, ...) keep permissive CORS for
browser-based API clients; they always require an API key.

Successful management key verifications against a bcrypt `secret-key` are
cached in memory for 5 minutes (at most 16 entries, bound to the configured hash
so changing the key invalidates them); failures are never cached and the
`allow-remote` and IP-ban checks run on every request.

## Web UI

The embedded UI at `/ui/` is a static single-page app that calls this API with
same-origin requests; it needs no extra endpoints. It is served without
authentication (assets only) unless `management.disable-ui` is true, in which
case `/ui/` returns 404. A request without any management key is answered with
`401` but does not count toward the failed-attempt ban, so the UI can probe for
tailnet identity access before asking for a key.

## Configuration endpoints

All paths below are relative to `/v8/management`.

| Path | Methods | Description |
| --- | --- | --- |
| `/config` | GET, PUT, PATCH | Read, replace, or merge the complete configuration as JSON. |
| `/config.yaml` | GET, PUT | Read or replace the complete configuration as YAML. |
| `/config/<section>/<field>` | GET, PUT, PATCH, DELETE | Read or modify a section or field using its path in the v8 tree. |

GET renders the persisted configuration in the v8 layout without migrating the
file or adding omitted runtime defaults. Successful v8 configuration writes save
the latest layout; reads and rejected writes leave the file unchanged.
Historical v8 paths and request bodies remain accepted as aliases.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
Scalar field updates retain existing comments. JSON writes also keep the
comments of entries that survive when a list or mapping is replaced: mapping
values match by key, list items by identity (a scalar's value, otherwise the
item's `alias`, `name`, `api-key` or `id`) or by position when unchanged.
Comments of removed or reworded entries are dropped. YAML replacements
(`/config.yaml`) use the submitted document, comments included.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Optional per-key `null` overrides inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/codex` | Codex upstream groups. |
| `/config/client/codex/optimize-multi-agent-v2` | Boolean, default `false`; applies to Codex clients across OAuth and API-key routes. |
| `/config/observability/logs/debug` | A boolean, for example `true`. |
| `/config/routing/retry/request-retry` | A number, for example `0`. |

For example, `PATCH /v8/management/config` with this body disables additional
request retries while preserving other settings:

```json
{
  "routing": {
    "retry": {
      "request-retry": 0
    }
  }
}
```

JSON reads omit TURN usernames and credentials under
`oauth.providers.codex.live-media-relay.ice-servers`; YAML reads include them.
JSON reads also omit `tailscale.auth-key`. JSON writes that omit it keep the
stored key; an explicit empty string or `null` clears it.

### Codex multi-agent configuration migration

`client.codex.optimize-multi-agent-v2` is the sole runtime setting. Loading older
YAML accepts `providers.codex.optimize-multi-agent-v2`,
`oauth.providers.codex.optimize-multi-agent-v2`, and the flat
`codex.optimize-multi-agent-v2` path. The client path always wins conflicts,
including explicit `false` or `null`. If it is absent, the historical OAuth path
wins over `providers`, which wins over the flat path.

These aliases now have client-wide semantics, not OAuth-only scope. Loading does
not rewrite legacy-only settings; normalization removes aliases conflicting with
the client path. A successful v8 write migrates the aliases while preserving their
comments. Historical API paths remain usable; older YAML payloads remain readable.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/config/upstream/<provider>` | GET, PUT, PATCH, DELETE | Manage shared provider settings. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/routing/slugs` | GET | List OpenAI-compatible aliases and their resolved upstream models. |
| `/routing/slugs/refresh` | POST | Re-poll follow-mode groups now. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/usage/api-keys` | GET | Get in-memory request counters for upstream API-key credentials. |
| `/observability/usage/summary` | GET | Usage totals for a window, optionally grouped (see below). |
| `/observability/usage/timeseries` | GET | Hour/day usage buckets for charts. |
| `/observability/usage/requests` | GET | Recent recorded upstream attempts, paginated. |
| `/observability/usage/pricing` | GET | Price catalog status (see Usage cost estimates). |
| `/observability/usage/pricing/refresh` | POST | Refetch the price catalog now. |
| `/observability/usage/pricing/catalog` | GET | Search catalog models for price mappings. |
| `/observability/usage/pricing/models` | GET | Models served in a window and how each is priced. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get credential models. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials (see below). |
| `/oauth/auth-url?provider=<provider>` | GET | Start OAuth. |
| `/oauth/siwc/bind` | POST | Start SIWC binding to an existing file-backed Codex OAuth credential. |
| `/oauth/status?state=<state>` | GET | Get login status. |
| `/oauth/session?state=<state>` | DELETE | Cancel a login session. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback. |
| `/tailscale/status` | GET | Embedded Tailscale node state (see below). |

### Credential responses

Credential endpoints never return token material. `GET /credentials` lists
status, account and usage fields only; for API-key credentials `account` is a
masked form of the key. `POST /credentials/refresh` with `{"name": ...}`
(optionally `auth_index`) returns a summary of the refreshed credential:

```json
{
  "ok": true,
  "auth": {
    "id": "claude-user@example.com.json",
    "auth_index": "0f1a98b0d7b03bbf",
    "name": "claude-user@example.com.json",
    "provider": "claude",
    "label": "user@example.com",
    "email": "user@example.com",
    "status": "active",
    "status_message": "",
    "disabled": false,
    "unavailable": false,
    "last_refresh": "2026-10-20T10:00:00Z",
    "next_refresh_after": "2026-10-20T17:00:00Z",
    "expires_at": "2026-10-20T18:00:00Z"
  }
}
```

Time fields are omitted when unknown. With `{"all": true}` the response is
`{"ok": true, "results": [{"id", "success", "error"}]}`. Both responses, and
the list, are sent with `Cache-Control: no-store`. Only
`GET /credentials/download` returns the raw credential file.

### Slugs (follow mode)

An `api-keys.openai-compatibility` model entry with `follow: true` keeps its
`alias` stable while the upstream model changes. The proxy polls
`GET {base-url}/models` (group's first key, headers and proxy; 10 s probe
timeout) immediately at startup and every `follow-interval` (default `30s`,
minimum `5s`), picks the first listed model matching `match` (or the first
listed model), and routes the alias to it. Resolution is in memory only; the
config file is never rewritten. A failed or empty poll keeps the last resolved
model. Follow entries always rewrite response `model` fields to the alias. A
non-empty `name` pins the upstream model and disables polling for that entry.
Until the first successful poll the alias is listed by `/v1/models`, but
requests to it fail with HTTP 503 and a message naming the slug.

`GET /routing/slugs` returns every alias of every enabled OpenAI-compatible
group in config order:

```json
{
  "slugs": [
    {
      "group": "dgx-spark",
      "alias": "DGX-SPARK",
      "base_url": "http://172.16.1.181:8888/v1",
      "mode": "follow",
      "resolved_model": "GLM-5.3-Flash-EXL3",
      "poll_interval": "30s",
      "last_check_at": "2026-10-20T18:00:30Z",
      "last_success_at": "2026-10-20T18:00:30Z",
      "reported_models": ["GLM-5.3-Flash-EXL3"]
    }
  ]
}
```

`mode` is `follow` (polled) or `pinned` (fixed `name`, including ordinary alias
entries). `resolved_model` is empty while a follow entry is unresolved.
`match` and `last_error` appear when set; `poll_interval`, `last_check_at`,
`last_success_at` and `last_error` only apply to groups that poll. Keys and
header values are never returned.

`POST /routing/slugs/refresh` polls all follow groups, or one group named by the
`group` query parameter or JSON field (`{"group":"dgx-spark"}`), waits until the
result is applied to routing, and returns the same body as the GET. An unknown
group returns 404.

### Usage history

Every upstream attempt (including retries and failovers) is recorded as one row
in a local SQLite database when `observability.usage.enabled` is true (the
default):

```yaml
observability:
  usage:
    enabled: true        # default true
    path: ""             # default: $WRITABLE_PATH/data/usage.db, else data/usage.db next to the config file
    retention-days: 90   # default 90; -1 keeps everything
    rollup-retention-days: 0 # totals and charts; default 0 keeps them forever
```

Besides the raw rows, every write also updates 15-minute rollups (one row per
bucket, dimension combination, context band and success flag), so summaries,
timeseries and costs scale with the number of buckets instead of requests.
Queries read whole buckets from the rollups and the partial buckets at either
window edge from the raw rows, so results are exact; once raw rows have been
pruned, an edge below the pruning point uses its whole 15-minute bucket
instead. Each query reads from a single database snapshot. Rollups have their own
retention (`rollup-retention-days`, never shorter than `retention-days`;
0 keeps them forever), so totals, costs and savings outlive per-request
details: windows older than `retention-days` still report totals, while the
request list and the p50/p95 latency only cover retained raw rows. Upgrading
backfills the rollups from existing rows once.

Relative paths resolve against the config file directory. In the container the
default is `/CLIProxyAPI/data/usage.db` (mount `./data`). Changes to these
keys apply on hot reload: the store is closed (pending rows flushed) and
reopened, or only the retention changes. Rows older than the retention are
pruned at startup and daily. Writes are asynchronous and never slow requests;
if the in-memory queue is full, rows are dropped and counted (`store.dropped`).
Requests rejected before reaching an upstream (for example, no credential
available) produce no row.

Rows never contain raw API keys. The client key is stored as an 8-hex-digit
SHA-256 fingerprint (`client_key_id`) plus a masked form (`client_key_masked`,
e.g. `sk-…abcd`; keys shorter than 8 characters are shown as `****`). Every
stored text field has the client key and the upstream credential's values
(API key, OAuth tokens) replaced with `[redacted]`; error summaries are reduced
to the upstream message, additionally scrubbed of token-like strings and
truncated to 300 characters. `session_id` is the first 16 hex digits of the
session ID's SHA-256 (enough to correlate requests). `base_url` loses
credentials, query and fragment; unparsable values are stored as `invalid-url`.
`auth_label` is the OAuth account email, or the credential's label with its
masked upstream key.

All three endpoints return HTTP 409 with
`{"error":"usage tracking is disabled","message":"..."}` when tracking is
disabled, and 503 when the database could not be opened.

Shared query parameters:

| Parameter | Meaning |
| --- | --- |
| `from`, `to` | Window as RFC3339 or unix milliseconds; `from` inclusive, `to` exclusive. Summary and timeseries default to the last 24 hours; requests default to all rows. |
| `tz` | IANA zone for hour/day buckets and output timestamps; default the server's zone. |
| `model` | Filter on `requested_model` (the alias or slug the client sent). |
| `upstream_model` | Filter on the resolved upstream model. |
| `provider` | Filter on provider (e.g. `claude`, `codex`, `openai-compatible-<group>`). |
| `credential` | Filter on `auth_id`. |
| `api_key` | Filter on `client_key_id`. |
| `success` | `true` or `false`. |

Every aggregate object (`totals`, groups, series totals, points) has:
`requests`, `successes`, `failures`, `input_tokens` (includes cache reads and
writes), `output_tokens` (includes reasoning), `reasoning_tokens`,
`cache_read_tokens`, `cache_creation_tokens`, `total_tokens`,
`avg_latency_ms` and `max_latency_ms` (successful requests only),
`avg_ttft_ms` (successful requests that reported a time to first token), and
`output_tps`, the generation speed in output tokens per second (reasoning
included). Speed covers successful streamed requests with a time to first
token and a generation window (`latency_ms - ttft_ms`) of at least 250 ms; a
shorter window means the server buffered the response, so it says nothing
about decode speed. It is the window-weighted rate (summed output tokens over
summed windows) and `0` when no request qualifies. Speed is recorded from
schema version 3 on; buckets older than the raw rows kept at upgrade have
none.

`GET /observability/usage/summary?group_by=` returns window totals, the
nearest-rank p50/p95 latency of successful requests, and, with `group_by`, one
group per value. `group_by` is one of `model`, `upstream_model`, `provider`,
`credential` (key `auth_id`, label `auth_label`), `api_key` (key
`client_key_id`, label `client_key_masked`), `day` or `hour` (keys are local
bucket starts). Dimension groups are sorted by requests, time groups
chronologically.

```json
{
  "from": "2026-10-19T19:07:14.194-05:00",
  "to": "2026-10-20T19:07:14.194-05:00",
  "timezone": "America/Chicago",
  "group_by": "model",
  "totals": {"requests": 4, "successes": 3, "failures": 1, "input_tokens": 45, "output_tokens": 23,
             "reasoning_tokens": 4, "cache_read_tokens": 8, "cache_creation_tokens": 0, "total_tokens": 68,
             "avg_latency_ms": 812.3, "max_latency_ms": 1904, "avg_ttft_ms": 230, "output_tps": 41.7},
  "latency_p50_ms": 640,
  "latency_p95_ms": 1904,
  "groups": [
    {"key": "SMOKE-ALIAS", "label": "SMOKE-ALIAS", "requests": 4, "successes": 3, "failures": 1, "...": "same fields as totals"}
  ],
  "store": {"dropped": 0, "retention_days": 30}
}
```

`GET /observability/usage/timeseries?bucket=hour|day&group_by=&top=` returns
zero-filled buckets for charts. `bucket` defaults to `hour`; a response may
contain at most 5000 buckets. Without `group_by` there is one series keyed
`total`. With a dimension `group_by` (not `day`/`hour`), the `top` (default 8,
max 50) values by request count get their own series and the rest are summed
into a series keyed `__other__` (label `other`). Every series has one point per
bucket, aligned with `buckets`.

```json
{
  "from": "2026-10-20T21:07:22.000Z",
  "to": "2026-10-21T00:07:22.234Z",
  "timezone": "UTC",
  "bucket": "hour",
  "group_by": "model",
  "buckets": ["2026-10-20T21:00:00.000Z", "2026-10-20T22:00:00.000Z", "2026-10-20T23:00:00.000Z", "2026-10-21T00:00:00.000Z"],
  "series": [
    {
      "key": "SMOKE-ALIAS",
      "label": "SMOKE-ALIAS",
      "totals": {"requests": 4, "...": "aggregate fields"},
      "points": [
        {"t": "2026-10-20T21:00:00.000Z", "requests": 0, "...": "aggregate fields"},
        {"t": "2026-10-21T00:00:00.000Z", "requests": 4, "total_tokens": 68, "...": "aggregate fields"}
      ]
    }
  ]
}
```

`GET /observability/usage/requests?limit=&before=` lists rows newest first
(by insertion, i.e. completion order). `limit` defaults to 50 (max 500). Pass
the returned `next_before` as `before` to get the next page; it is `null` on
the last page. `ts` is the attempt start time in UTC.

```json
{
  "requests": [
    {
      "id": 4,
      "ts": "2026-10-21T00:07:08.573Z",
      "request_id": "b848b95a-6949-4064-a985-6990ea85cdf3",
      "trace_id": "01a15bc8-7e1d-7052-b11d-9c17d997cff1",
      "provider": "openai-compatible-fakeup",
      "auth_id": "openai-compatibility:fakeup:3e3e96a147be",
      "auth_label": "fakeup (sk-…6789)",
      "auth_type": "apikey",
      "requested_model": "SMOKE-ALIAS",
      "upstream_model": "fake-upstream-model",
      "base_url": "http://127.0.0.1:18799/v1",
      "client_key_id": "9a6099d7",
      "client_key_masked": "smo…5b1d",
      "endpoint": "/v1/chat/completions",
      "stream": false,
      "success": false,
      "status": 500,
      "error": "upstream exploded; key [redacted] rejected",
      "latency_ms": 12,
      "ttft_ms": 0,
      "input_tokens": 0,
      "output_tokens": 0,
      "reasoning_tokens": 0,
      "cache_read_tokens": 0,
      "cache_creation_tokens": 0,
      "total_tokens": 0,
      "service_tier": "auto"
    }
  ],
  "next_before": 4
}
```

Optional row fields (`trace_id`, `auth_type`, `response_model`, `base_url`,
`endpoint`, `session_id`, `error`, `reasoning_effort`, `service_tier`) are
omitted when empty. `output_tps` (the row's generation speed, see above) is
omitted when the row does not qualify.

### Usage cost estimates

When `observability.usage.pricing.enabled` is true (the default), usage
responses include API-equivalent costs at [models.dev](https://models.dev)
list prices. Costs are computed at query time from the current catalog and
mappings, so changing a mapping re-prices the whole history.

```yaml
observability:
  usage:
    pricing:
      enabled: true          # default true
      source: ""             # default https://models.dev/api.json; or an absolute file path
      models:                # for aliases, slugs and local models
        - model: gpt-oss-120b             # requested, upstream or response model ID (case-insensitive)
          price-as: zai/glm-5.3-flash     # a models.dev "provider/model" ID
        - model: gpt-oss-120b             # a later price for the same model
          input: 0.6
          output: 0.9
          from: "2026-10-21T15:00:00Z"    # applies to requests from this time on
        - model: my-finetune
          input: 0.5                      # USD per million tokens
          output: 1.5
          cache-read: 0.05                # optional; defaults to the input price
          cache-write: 0.5                # optional; defaults to the input price
      subscriptions:         # monthly plan fees, per OAuth account (auth ID)
        - account: claude-me@example.com.json
          provider: claude
          monthly-usd: 200
      provider-classes:      # override the detected cost class
        openai-compatible-dgx-spark: local
```

The catalog is cached next to the usage database (`models-dev.json`) and
refetched daily; on failure the last catalog is kept and retried hourly.

**Matching.** Explicit `models` entries win, checked against the upstream,
response and then requested model: a mapping of the served model beats a
mapping of the alias, so a model behind a follow-mode slug keeps its own price
after the slug moves to another model. Entries for the same model with
different `from` times form a price history; each request uses the entry in
effect when it was made (to 15-minute precision), and requests before the
earliest `from` use the automatic price. Otherwise the served model (upstream, then
response; the requested alias only when neither was recorded) is looked up
under the provider's models.dev home (`claude` → `anthropic`, `codex` →
`openai`, `xai`, `kimi` → `moonshotai`, `meta`), then first-party providers,
then any provider when all listings agree on price. Lookups also try the ID
without a `[1m]`/`(high)` suffix, an organization prefix, a date suffix and
`-thinking`.

**Context bands.** Rollups split requests into input-size bands at 100k,
200k, 272k and 512k tokens, the first-party tier sizes; a tier at another size
(e.g. 32k, 128k or 256k on some hosts) is applied to the whole enclosing band.

**Formula.** Uncached input (`input_tokens` minus cache reads and writes),
cache reads, cache writes and output are priced separately; reasoning tokens
are billed as output unless the model has a separate reasoning price. Context
tiers (e.g. above 272k input tokens) apply per request, and a response
`service_tier` such as `priority` uses the model's matching mode price.

**Classes.** Each row has a cost class: `subscription` for OAuth accounts,
`local` for OpenAI-compatible providers whose base URL is loopback, private,
link-local, Tailscale CGNAT, a single-label host or `.local`/`.lan`/`.home`/
`.internal`/`.ts.net`, and `api` (real spend) otherwise.

Every aggregate object gains `cost_usd` (all priced rows), `api_spend_usd`,
`subscription_value_usd`, `local_value_usd` and `unpriced_requests`. Request
rows gain `cost_usd` (omitted when unpriced), `cost_class` and `priced_as`.
Summaries add:

```json
"pricing": {
  "catalog": {"enabled": true, "source": "https://models.dev/api.json", "fetched_at": "2026-10-21T08:30:00Z", "models": 8726},
  "subscription_fees_usd": 6.57,
  "subscriptions": [{"account": "claude-me@example.com.json", "provider": "claude", "monthly_usd": 200, "window_usd": 6.57}]
}
```

`window_usd` prorates the monthly fee over the query window (average month);
fees honor the `provider` and `credential` filters. Pass `cost=false` to skip
cost estimation on a usage query.

`GET /observability/usage/pricing` returns the catalog status (`enabled`,
`source`, `fetched_at`, `models`, `error`). `POST .../pricing/refresh` returns
202, 409 when pricing is disabled, or 429 within a minute of the last attempt.
`GET .../pricing/catalog?q=&limit=` (limit default 25, max 100) returns
`{"models": [{"id", "provider", "provider_name", "model", "name", "price"}]}`,
where `price` has `input`, `output`, `cache_read`, `cache_write`, optional
`reasoning`, `tiers` and `modes`; it returns 503 until a catalog has loaded.
`GET .../pricing/models` (shared window and filter parameters; default 30 days)
returns `{"models": [{"provider", "requested_model", "upstream_model",
"response_model", "class", "priced_as", "source": "auto"|"override", "price",
"from", "history", "requests", "total_tokens", "cost_usd"}]}`; `from` and
`history` (every `{from, priced_as, source, price}`, oldest first) are present
for dated prices. `GET .../pricing/unpriced-local` returns
`{"models": [{"provider", "group", "alias", "model"}]}`: models configured on
your own hardware (OpenAI-compatible providers in the `local` class, follow
slugs resolved to the model they serve now) that have no price; the UI uses it
to prompt for a price after a model swap. Summaries report
`store.rollup_retention_days`.

### Tailscale status

`GET /tailscale/status` reports the embedded Tailscale node and the caller:

```json
{
  "enabled": true,
  "active": true,
  "restart_required": false,
  "state": "Running",
  "hostname": "cliproxy",
  "dns_name": "cliproxy.tail1234.ts.net",
  "tailnet": "you@github",
  "magic_dns_suffix": "tail1234.ts.net",
  "magic_dns_enabled": true,
  "ips": ["100.101.102.103", "fd7a:115c:a1e0::1"],
  "https_enabled": true,
  "cert_domains": ["cliproxy.tail1234.ts.net"],
  "urls": ["https://cliproxy.tail1234.ts.net"],
  "self_user": "you@github",
  "caller": {
    "via_tailnet": true,
    "login_name": "you@github",
    "node_name": "laptop.tail1234.ts.net",
    "tagged": false,
    "management_identity": true
  }
}
```

- `enabled` is the configured `tailscale.enabled`; `active` reports whether this
  process runs a node. `restart_required` is true when the configured settings
  (other than `management-users`) differ from the running node's.
- `state` is the Tailscale backend state (`NoState`, `NeedsLogin`,
  `NeedsMachineAuth`, `Starting`, `Running`, `Stopped`), or `Disabled` when no
  node runs. While it is `NeedsLogin`, `auth_url` holds the interactive login URL.
- `https_enabled` is the configured `tailscale.https`. An empty `cert_domains`
  means HTTPS certificates are not enabled in the tailnet admin console, so the
  HTTPS listener cannot complete TLS handshakes. `http_port` is present when a
  plain HTTP tailnet listener runs.
- `self_user` is the node owner's login name; nodes joined with a tagged auth key
  report `tags` instead. Fields without a value are omitted.
- `caller.via_tailnet` reports whether the request arrived on a tailnet
  listener; the identity fields are present only then.
  `caller.management_identity` reports whether this request qualified for
  identity authorization, including the origin and host checks under Access.

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `kimi`, `kimi-ai`, `xai`, `devin`, or `meta`. For example:

```http
GET /v8/management/oauth/auth-url?provider=codex
Authorization: Bearer <management-key>
```

Keep any provider-specific login parameters in the query string. The login
response includes the authorization URL and a session `state`. Use that state
for status queries and cancellation.

For `claude` and `codex`, add `is_webui=true` (also `1`, `yes`, `on`) to start a
temporary callback forwarder on the provider's fixed loopback redirect port
(Claude `54545`, Codex `1455`). The forwarder redirects the browser to
`http://127.0.0.1:<server.port>/anthropic/callback` or `/codex/callback`, so it
only helps when the browser runs on the proxy host (in Docker: the host, with
those ports and the server port published). Without `is_webui` no listener is
started. In every other case, submit the final redirect URL to
`POST /oauth/callback` as `{"redirect_url": "..."}`.

Callbacks accept `provider`, `state`, `code`, and `error` as GET query parameters
or POST JSON fields. POST also accepts `redirect_url` containing the complete
callback URL. If `provider` is omitted, it is inferred from the pending state;
an explicit provider must match that state. Poll until the status is `ok` or
`error`; `wait` means the login is still pending. Callback acceptance alone does
not mean credential exchange and persistence have completed.

### Sign in with ChatGPT (SIWC) binding

SIWC is a second credential partition of an existing Codex credential, not a new
provider. It requires a file-backed Codex OAuth credential in the auth directory.

Start a binding with management authentication:

```http
POST /v8/management/oauth/siwc/bind
Authorization: Bearer <management-key>
Content-Type: application/json

{"name":"codex-example.json"}
```

`name` identifies an existing managed credential by its ID or file name; it is not
an arbitrary filesystem path. The credential must contain nonempty outer Codex
access and refresh tokens. A successful start returns:

```json
{
  "status": "ok",
  "url": "https://auth.openai.com/api/accounts/authorize?...",
  "state": "<random-state>",
  "flow": "siwc_bind",
  "expires_in": 300
}
```

Opening the returned URL starts authorization. Its redirect URI is fixed to
`http://127.0.0.1:1455/auth/callback`. This endpoint does not start a listener on
port 1455: for remote deployments, paste the complete URL from the browser address
bar, even if the loopback page cannot be reached, and submit it to the server:

```http
POST /v8/management/oauth/callback
Content-Type: application/json

{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=<state>&code=<code>&client_id=<issued-client-id>"}
```

The v8 callback also accepts `state`, `code`, and `client_id` directly as POST JSON
fields or GET query parameters. SIWC requires the **issued** `client_id` from the
callback; the authorization URL's `dynamic_agent_client` is not a substitute.
Error callbacks can supply `error` instead of `code` and `client_id`. Conflicting
parameters are rejected. The target, PKCE verifier, device ID, and redirect URI
are retained on the server and cannot be changed by a callback. Callback values
are handed directly to the SIWC session rather than written to legacy callback
files. Neither callbacks nor status responses return tokens.

Poll `GET /oauth/status?state=<state>` until `ok` or `error`. `wait` means binding
has not completed, including while tokens are being exchanged. Success means the
new `siwc` partition and `use_siwc=true` have been saved and published to the auth
manager. Other credential fields are retained. Rebinding explicitly replaces the
SIWC partition and reuses a valid existing device ID.

`DELETE /oauth/session?state=<state>` cancels a pending binding. Cancellation that
wins the commit gate prevents saving a late token response; cancellation after
commit returns `cancelled:false`. Duplicate callbacks and a second pending binding
for the same credential return `409`. Missing targets return `404`; ineligible
targets and incomplete callbacks return `400`. Deleting or replacing the target
through the v8 credential API invalidates its pending binding. An old server
returns `404` for the start endpoint; clients must not fall back to ordinary Codex
login. Sessions are process-local, expire after five minutes, and do not survive
restart.

For Codex entries, `GET /credentials` includes these non-secret summary fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `siwc_bind_supported` | boolean | This credential is eligible for the binding flow. Missing means unsupported. |
| `use_siwc` | boolean | Requests are configured to use the nested SIWC credential. |
| `siwc_bound` | boolean | SIWC access token, refresh token, and issued client ID are present; not a live validity check. |
| `siwc_expires_at` | optional RFC3339 string | Stored SIWC expiration, when available. |

SIWC activation,
unbinding, and standalone SIWC credential creation are not separate management
operations in this release.

## Legacy configuration files

Legacy-only configuration files keep their layout until a successful v8
configuration write. When both layouts specify a field, the new field takes
precedence and its legacy equivalent is removed. Settings for removed features
(for example `server.discovery`, `server.github-token`, the management control
panel keys, the old `observability.usage.usage-statistics-enabled` and
`redis-usage-queue-retention-seconds` keys, or `api-keys` groups for removed providers)
are ignored at startup and archived as YAML comments on the next v8 write.
