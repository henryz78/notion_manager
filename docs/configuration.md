# Configuration

[← Back to README](../README.md)

## Priority

```text
environment variables > config.yaml > code defaults
```

### Config file location and Railway persistence

Local launches continue to use the repository `config.yaml`. When
`ACCOUNTS_DIR` is set (the Docker/Railway default is `/app/accounts`), the
process seeds `${ACCOUNTS_DIR}/.notion-manager-config.yaml` on first startup and
writes Dashboard changes there. The file lives beside the account volume, so
settings survive Railway sleep, container replacement, and image updates. Set
`CONFIG_PATH` to choose an explicit file; it takes precedence over the automatic
path.

Railway needs both `ACCOUNTS_DIR=/app/accounts` and an actual Volume mounted at
`/app/accounts`. An environment variable without the Volume does not preserve
files across container replacement. Environment variables such as
`USE_NOTION_PERSONAL_INSTRUCTIONS` remain higher priority than Dashboard values.

## Server

| Setting | Repo sample | Code default | Notes |
| --- | --- | --- | --- |
| `server.port` | `3000` | `8081` | Listening port |
| `server.accounts_dir` | `accounts` | `accounts` | Account JSON directory; also stores `.register_history.json` / `.token_stats.json` / `.register_inputs/` |
| `server.token_file` | `token.txt` | `token.txt` | Fallback single-account file; container deployments with `ACCOUNTS_DIR` move it into the persistent volume |
| `server.api_key` | empty | auto-generated | Used by `/v1/messages` |
| `server.admin_password` | empty | auto-generated | Hashed on first startup; override with `ADMIN_PASSWORD` |
| `server.log_file` | `server.stderr.log` | empty (stderr) | All `log.Printf` output is appended here |
| `server.debug_logging` | `true` | `true` | High-volume debug logs |
| `server.api_log_input` | `true` | `false` | Log incoming `/v1/messages` request bodies |
| `server.api_log_output` | `true` | `false` | Log responses returned to API clients |
| `server.notion_log_request` | `true` | `false` | Log `runInferenceTranscript` request bodies |
| `server.notion_log_response` | `true` | `false` | Log raw inference responses |
| `server.dump_api_input` | `false` | `false` | Dump system prompts + tool schemas to disk per request (Claude Code debugging) |

## Proxy / runtime behaviour

| Setting | Repo sample | Code default | Notes |
| --- | --- | --- | --- |
| `proxy.notion_api_base` | `https://www.notion.so/api/v3` | same | Notion API base URL |
| `proxy.client_version` | `23.13.20260313.1423` | same | `x-notion-client-version` header |
| `proxy.default_model` | `claude-opus-4.6` | `claude-opus-4.6` | Fallback model when request omits one |
| `proxy.disable_notion_prompt` | `true` | `false` | Removes Notion's ~33k system prompt for leaner API usage |
| `proxy.use_client_system_prompt` | `true` | `true` | Include the client-supplied System Prompt; independent of Notion personal instructions |
| `proxy.use_notion_personal_instructions` | `false` | `false` | Use the selected account's default Notion Agent instructions page; only the page ID is read, never its contents |
| `proxy.enable_tool_bridge` | `true` | `true` | Translate external Tools/functions into the compatibility format used by Claude Code and other clients |
| `proxy.enable_web_search` | `true` | `true` | Global web-search toggle (overridable per-request) |
| `proxy.enable_workspace_search` | `false` | `false` | Global workspace-search toggle (overridable per-request) |
| `proxy.ask_mode_default` | unset | `false` | When `true`, every chat runs with `useReadOnlyMode=true` (Notion's "Ask" toggle) — the per-request `-ask` suffix overrides this |
| `proxy.notion_proxy` | empty | empty | Upstream proxy (`http`/`https`/`socks5`/`socks5h`) used for **all** Notion-bound dials, including bulk register, `/ai`, and `/v1/messages` |

## Timeouts / refresh

| Setting | Repo sample | Code default | Notes |
| --- | --- | --- | --- |
| `timeouts.inference_timeout` | `120` | `120` | Maximum idle time between standard inference events (s); continuous output is not cut off |
| `timeouts.research_timeout` | unset | `360` | Maximum idle time between research events (s) — applies to `researcher` / `fast-researcher` |
| `timeouts.api_timeout` | `30` | `30` | Quota / models REST timeout (s) |
| `timeouts.tls_dial_timeout` | `30` | `30` | TLS dial timeout (s) |
| `refresh.interval_minutes` | `30` | `30` | Background refresh interval |
| `refresh.quota_recheck_minutes` | `30` | `30` | Min interval between rechecks of an exhausted account |
| `refresh.concurrency` | `10` | `10` | Concurrent refresh workers |
| `refresh.live_check_seconds` | unset | `5` | Min interval between live per-request quota checks for the same account; `0` = check on every request |

## Register (bulk MS SSO)

| Setting | Code default | Notes |
| --- | --- | --- |
| `register.history_file` | `accounts/.register_history.json` | Persisted job history; container deployments with `ACCOUNTS_DIR` map it into the persistent volume |
| `register.history_memory_cap` | `100` | Max jobs kept in RAM (and on disk) |
| `register.default_concurrency` | `1` | Pre-filled value in the dashboard register modal |

## Environment variables

Every `server.*`, `proxy.*`, `timeouts.*`, `refresh.*`, `browser.*` field above has an equivalent env var (uppercased, dotted segments joined by `_`). The most useful ones:

```bash
export PORT=3000
export API_KEY=sk-your-api-key
export ADMIN_PASSWORD=your-dashboard-password
# Optional dashboard version override; Railway repository deploys normally expose the commit automatically
export APP_VERSION=your-version
# Optional dedicated signing key; ADMIN_PASSWORD is used when omitted.
export DASHBOARD_SESSION_SECRET=your-stable-random-secret
export ENABLE_WEB_SEARCH=true
export ENABLE_WORKSPACE_SEARCH=false
export ASK_MODE_DEFAULT=false
export USE_CLIENT_SYSTEM_PROMPT=true
export USE_NOTION_PERSONAL_INSTRUCTIONS=false
export ENABLE_TOOL_BRIDGE=true
export NOTION_PROXY=socks5h://127.0.0.1:1080
export QUOTA_LIVE_CHECK_SECONDS=5
export REFRESH_CONCURRENCY=10
export CONFIG_PATH=/app/accounts/.notion-manager-config.yaml # optional; automatic when ACCOUNTS_DIR is set
```

An invalid `NOTION_PROXY` is logged once at startup and dropped — runtime falls back to direct dial rather than leaking the misconfigured URL into every Notion request.

## Endpoints

| Path | Purpose | Auth |
| --- | --- | --- |
| `GET /health` | Health and account pool summary | None |
| `POST /v1/messages` | Anthropic Messages API | API key |
| `GET /dashboard/` | Dashboard UI | Dashboard login |
| `GET /proxy/start` | Create a targeted proxy session | Dashboard login |
| `GET /ai` | Local Notion Web proxy entry | `np_session` |
| `GET /admin/accounts` | Pool list (supports `q`, `page`, `page_size`) | Dashboard session |
| `DELETE /admin/accounts/{email}` | Remove account file + pool entry | Dashboard session |
| `GET /admin/models` | Model mapping + pool-discovered models | Dashboard session |
| `GET/POST /admin/refresh` | Refresh status / trigger refresh | Dashboard session |
| `GET/PUT /admin/settings` | Read / update runtime settings (`enable_web_search`, `enable_workspace_search`, `ask_mode_default`, `use_client_system_prompt`, `use_notion_personal_instructions`, `enable_tool_bridge`, `debug_logging`, `notion_proxy`) | Dashboard session |
| `GET /admin/stats` | Token usage statistics (lifetime + today + 24h + 30-day series + top-N) | Dashboard session |
| `POST /admin/register` | Legacy synchronous bulk register (kept for back-compat) | Dashboard session |
| `GET /admin/register/providers` | List registered Providers | Dashboard session |
| `POST /admin/register/start` | Start a new bulk-register Job | Dashboard session |
| `GET /admin/register/jobs[?limit=]` | Recent Job list | Dashboard session |
| `GET /admin/register/jobs/{id}` | Single Job snapshot | Dashboard session |
| `GET /admin/register/jobs/{id}/events` | SSE event stream | Dashboard session |
| `POST /admin/register/jobs/{id}/retry` | Retry only the failed steps | Dashboard session |
| `DELETE /admin/register/jobs/{id}` | Drop Job + sidecar | Dashboard session |

## Project layout

```text
notion-manager/
├── cmd/notion-manager/        # Server entrypoint
├── cmd/register/              # Standalone bulk-register CLI
├── internal/proxy/            # Account pool, API handler, reverse proxy, uploads, config, stats
├── internal/msalogin/         # Microsoft consumer SSO + Notion onboarding flow
├── internal/regjob/           # Provider-pluggable bulk-register runner + store
├── internal/regjob/providers/ # Provider implementations (microsoft/, ...)
├── internal/netutil/          # Proxy dialer helpers
├── internal/web/dist/         # Embedded Dashboard static assets
├── web/                       # Dashboard frontend source (React + Vite)
├── chrome-extension/          # Chrome extension that extracts Notion sessions
├── accounts/                  # Account JSON files + .register_history.json + .token_stats.json
├── docs/                      # English + Chinese documentation
├── example.config.yaml
├── README.md
└── README_CN.md
```

## Notes

- `admin_password` left empty auto-generates a random password printed to the console, then hashes and writes it back. Save the plaintext shown on first startup — it cannot be recovered.
- Reverse proxy works best with account files that include `full_cookie`; Microsoft-SSO registrations come with a working `full_cookie` out of the box.
- Free / Plus complimentary trials can stay unavailable after exhaustion, but account files are retained and rechecked so a later Business / Enterprise upgrade can recover automatically.
- If you change the dashboard source under `web/`, run `npm run build` inside `web/` and copy the output into `internal/web/dist/`.
- `accounts/.register_inputs/<job_id>.json` contains plaintext credentials and **must not be checked into version control** — `.gitignore` already excludes the directory.
