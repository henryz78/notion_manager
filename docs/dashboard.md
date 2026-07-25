# Dashboard & Proxy

[← Back to README](../README.md)

## Auth model

Two distinct auth surfaces:

- `/v1/messages` — **API key** auth (`Authorization: Bearer <key>` or `x-api-key: <key>`), for external clients
- `/admin/*`, `/dashboard/`, `/proxy/start` — **dashboard session** auth (cookie set by `/dashboard/auth/login`)

After signing in through `/dashboard/`, the frontend uses the `dashboard_session` cookie to access:

- `/admin/accounts` (incl. `DELETE /admin/accounts/{email}`)
- `/admin/models`
- `/admin/refresh`
- `/admin/settings`
- `/admin/stats`
- `/admin/register` (legacy synchronous) and `/admin/register/*` (Job-based)
- `/proxy/start`

Login uses client-side SHA256(salt + password) so the plaintext password never traverses the network — see `internal/proxy/dashboard.go`. A signed, HttpOnly dashboard cookie remains valid for 30 days and survives Railway sleep/restarts; signing-secret changes and explicit logout invalidate it.

## Page layout

The dashboard header now switches between two focused pages:

- **Accounts** — pool summary, quota diagnostics, refresh/add/register/cleanup actions, search, and account cards
- **Settings & history** — API key/base URL, global proxy, feature toggles, request history, registration jobs, and the running build version

The header and settings page display the current short commit. The HTML shell is
served with `no-store`, while hashed JS/CSS assets remain immutable. The full
version is also available in the `X-Notion-Manager-Version` response header on
`GET /health`.

## Pool view

`/dashboard/` lists every account in the pool with:

- Email, plan type, workspace
- Private-API diagnostic counters (basic / premium / space / user / research), clearly labeled as non-public schema
- Discovered models, last-checked timestamp
- Default Notion Agent personal-instructions status: configured, missing, failed, or not checked
- Per-row actions: **Open proxy**, **Copy token**, **Disable/enable account**, **Delete account**
- Filter by available, disabled, exhausted, invalid-cookie, no-workspace, temporarily unavailable, or personal-instructions state. `GET /admin/accounts/selection` returns only emails matching the current search/filter for true select-all across every page; it never returns tokens
- Account checkboxes support cross-page selection. Bulk actions: copy emails, check selected personal instructions, disable, enable, delete, and clear selection
- Checks, enable/disable, deletion, missing-instructions cleanup, and exhausted-trial cleanup use `/admin/account-batch-jobs`. Jobs run with 10 workers by default (maximum 20), expose live progress, survive browser refresh, and allow retrying failed items
- Only one pool-wide batch job may run at a time. A second dashboard tab receives and displays the existing job instead of starting duplicate mutations
- The personal-instructions probe reads only whether a page is bound; it never loads or stores the page ID or instruction text. Missing-instructions cleanup re-checks each account and retains probe failures
- Bulk cleanup for Free / Plus accounts explicitly disabled after exhausting complimentary AI responses. It requires confirmation and excludes Business / Enterprise, temporary failures, invalid cookies, and no-workspace accounts

Manual disable persists `disabled: true` in the matching account JSON. The
account remains visible and can still be re-enabled, but every API/proxy
account picker skips it while disabled.

The latest 20 batch-job snapshots are stored in `accounts/.account_batch_jobs.json`. Completed history remains after a Railway restart. A task running at the instant of a service restart is marked interrupted, and its unfinished accounts can be retried. Add Account accepts one token per line and validates up to five concurrently. The import modal can keep all accounts or only accounts with a configured default-Agent binding.

The list is fetched from `GET /admin/accounts?q=&status=&page=&page_size=`. The Go server filters by query/status, sorts by health, then paginates, so big pools (1k+ accounts) stay responsive. The response includes a pool-wide `summary` block (premium count, total remaining, etc.) for the headline cards regardless of pagination.

When `q`/`status`/`page`/`page_size` are all absent, the response keeps its historical shape (full unsorted list) so older scripts and curl pipelines stay happy.

## Headline cards

Pool-wide aggregates rendered from the `summary` block:

- Total / available / exhausted / no-workspace / premium accounts
- Full Notion AI plan signals versus complimentary-trial accounts
- Estimated basic remainder and raw premium/user/space values, explicitly not presented as public product guarantees

Notion's public docs do not publish a fixed complimentary-response count or a
numeric Research Mode trial cap. Custom Agents' Notion credits are separate and
are not included in these private default-Agent counters.

## Token usage statistics

A dedicated panel reading `GET /admin/stats`. Shows:

- **Total** lifetime input + output tokens, total request count
- **Today** and **last 24h** rolling windows (the 24h figure linearly interpolates yesterday's bucket against the current time-of-day)
- **30-day daily series** rendered as a line chart
- **Top-5 models** and **top-5 accounts** by total tokens

Counters survive restarts via `accounts/.token_stats.json`. The flush loop runs every 5 s.

## Bulk register drawer

Top-right `+ Register` button. Streams a job's progress live. See [Bulk Registration](registration.md) for the full protocol; in summary:

1. Pick a Provider (Microsoft is the default)
2. (Optional) set per-job concurrency and upstream proxy
3. Paste credentials (`email----password----client_id----refresh_token`, even-row count)
4. **Start** kicks off the run; the drawer streams `event: snapshot`, `event: step`, `event: done` over `/admin/register/jobs/{id}/events`
5. Failed rows can be retried in one click — credentials come from the server-side sidecar; the dashboard never re-asks for the secret

A history drawer (`History` button) shows the most recent jobs from `/admin/register/jobs`, with per-job snapshot, retry, and delete actions.

## Settings & history page

Editable knobs are persisted into the active config file: local launches use
`config.yaml`; Docker/Railway deployments with `ACCOUNTS_DIR` use
`accounts/.notion-manager-config.yaml`, so the settings live on the Volume:

- `enable_web_search`, `enable_workspace_search`
- `ask_mode_default` — when ON, every request behaves as if the user toggled Notion's "Ask" (read-only). The per-request `-ask` model suffix overrides this for one call
- `use_client_system_prompt` and `use_notion_personal_instructions` are independent: either, both, or neither may be enabled
- `enable_tool_bridge` separately controls external Tools/function-call compatibility. Turning it off leaves normal chat active but client tools are treated as plain requests
- `debug_logging`
- `notion_proxy` — paste an `http`/`https`/`socks5`/`socks5h` URL to tunnel **all** Notion-bound traffic. Bad schemes are rejected with a 400; clearing the field reverts to direct dial. Idle pooled connections are dropped on save so the next dial picks up the new upstream

## Opening the local Notion proxy

1. Click the best account or a specific account in the dashboard
2. The browser hits `/proxy/start?best=true` or `/proxy/start?email=<email>`
3. The server creates `np_session`, then redirects to `/ai`
4. Notion HTML, API requests, assets, and realtime connections all flow through that account
5. Accounts whose Notion workspace is missing return `409` instead of redirecting (the dashboard surfaces a "no workspace" badge so the user picks another account)

The reverse proxy auto-handles:

- Injecting `full_cookie` (or the minimal cookie set when `full_cookie` isn't present)
- Forwarding `/_assets/*`, `/api/*`, `/primus-v8/*`, `/_msgproxy/*`
- Rewriting Notion frontend base URLs (`CONFIG.domainBaseUrl`, etc.)
- Stripping analytics scripts (GTM, customer.io, …)

## Account ops

- **Delete** — `DELETE /admin/accounts/{email}` removes the matching JSON file from `accounts/` and drops the live pool entry. Useful for retired accounts so they don't poison the picker
- **Refresh** — `POST /admin/refresh` runs the quota / models check across the whole pool. The endpoint returns `started: false` if a refresh is already in flight
- **Settings** — `PUT /admin/settings` is idempotent and persists to the active config file via YAML node manipulation (so comments survive)
