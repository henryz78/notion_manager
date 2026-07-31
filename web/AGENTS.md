# notion-manager Dashboard — Frontend Development Guide

## Tech Stack

- **Framework**: React 19 + TypeScript
- **Build**: Vite 6
- **Styling**: TailwindCSS v4 (via `@tailwindcss/vite` plugin)
- **Embedding**: Go `//go:embed` — compiled dist is embedded into the Go binary

## Project Structure

```
web/
├── index.html          # HTML entry point
├── package.json        # Dependencies
├── vite.config.ts      # Vite config (base: /dashboard/, proxy to :8081)
├── tsconfig.json       # TypeScript config
└── src/
    ├── main.tsx        # React root mount
    ├── App.tsx         # Main dashboard component (Header, Summary, Grid)
    ├── api.ts          # API calls (/admin/accounts, /proxy/start)
    ├── types.ts        # TypeScript interfaces (AccountInfo, DashboardData, Model)
    ├── utils.ts        # Helpers (avatarColor, quota status, formatting)
    ├── index.css       # TailwindCSS imports + theme variables
    └── vite-env.d.ts   # Vite type reference
```

## Build & Deploy

```bash
# Development (hot reload, proxies API to localhost:8081)
cd web && npm run dev

# Production build
make web          # builds frontend + copies to internal/web/dist/
make build        # builds frontend + Go binary
make build-go     # Go binary only (uses existing internal/web/dist/)
```

The build pipeline:
1. `npm run build` → `web/dist/`
2. `xcopy web/dist internal/web/dist/` (embedded into Go binary)
3. `go build` with `//go:embed dist/*` in `internal/web/embed.go`
4. Go serves `/dashboard/` with only public version metadata in the HTML; after Dashboard authentication, the SPA fetches a masked API key from `/admin/api-key` and requests the full value only for reveal/copy actions

## Design System

### Theme (Notion Dark)

All theme colors are defined as CSS custom properties in `src/index.css` under `@theme`:

| Token              | Value                    | Usage                     |
|--------------------|--------------------------|---------------------------|
| `--color-bg-primary`   | `#191919`            | Page background           |
| `--color-bg-secondary` | `#202020`            | Header background         |
| `--color-bg-card`      | `#252525`            | Card background           |
| `--color-bg-card-hover`| `#2f2f2f`            | Card hover state          |
| `--color-bg-exhausted` | `#2a1f1f`            | Exhausted account card bg |
| `--color-text-primary` | `#ebebea`            | Primary text              |
| `--color-text-secondary`| `#9b9a97`           | Secondary/label text      |
| `--color-text-muted`   | `#5a5a5a`            | Muted/timestamp text      |
| `--color-notion-blue`  | `#2383e2`            | Accent / links / buttons  |
| `--color-ok`           | `#4dab9a`            | Available / healthy       |
| `--color-warn`         | `#d9a651`            | Low quota warning         |
| `--color-err`          | `#eb5757`            | Exhausted / error         |
| `--color-research`     | `#b39ddb`            | Research mode badge       |

Use TailwindCSS utility classes with these tokens, e.g. `bg-bg-card`, `text-text-primary`, `text-ok`.

### Typography

- Font: system font stack (`-apple-system, BlinkMacSystemFont, "Segoe UI", ...`)
- Summary stat values: `text-2xl font-bold tabular-nums`
- Card title: `text-[13px] font-semibold`
- Labels: `text-[11px] text-text-secondary uppercase tracking-wider`
- Timestamps: `text-[10px] text-text-muted`

### Components

- **StatCard**: Summary metric with label, value, sub-text
- **TotalQuotaBar**: Full-width progress bar with color-coded fill
- **AccountCard**: Clickable card with avatar, badges, quota bar, model pills
- **Badge**: Small pill with variant styling (`paid`, `free`, `research`, `exhausted`, `ok`, `model`)
- **QuotaBar**: Thin progress bar with color based on usage percentage

### Quota Display Convention

**Important**: Quota bars and numbers show **已使用 / 总额度** (used / total) format.
- The progress bar fill represents **usage** (how much has been consumed)
- Color coding: green (<70%), yellow (70-90%), red (>90%)
- Do NOT label as "配额" alone — it's ambiguous. Use "已使用" or "used / total" phrasing
- The summary card shows **剩余** (remaining) which is `total - used`

### Account Status

| Status    | Dot Color | Card Style              | Badge       |
|-----------|-----------|-------------------------|-------------|
| Available | Green     | Default bg              | `✓ 可用`    |
| Low quota | Yellow    | Default bg              | `✓ 可用`    |
| Exhausted | Red       | `bg-bg-exhausted`       | `⛔ 耗尽`   |
| Permanent | Red       | `bg-bg-exhausted` + 55% opacity | `⛔ 耗尽` |

### Research Mode

Current public Notion documentation guarantees full Research Mode on
**Business and Enterprise**. Free and Plus may expose a limited trial, but the
public docs do not publish a fixed numeric cap or reset rule.
- Never render a hard-coded `/3` limit.
- Display `research_mode_usage` only as a private-API diagnostic value.
- Research mode uses request type `"researcher"` instead of `"workflow"`.
- Custom Agents' Notion credits are separate from default Notion Agent usage.

## API Integration

### Data Source

The settings page exposes three independent request-processing switches:
`use_client_system_prompt`, `use_notion_personal_instructions`, and
`enable_tool_bridge`. Do not render them as a mutually-exclusive mode picker.

The dashboard fetches from `GET /admin/accounts` with the dashboard
session cookie (or `Authorization: Bearer <api-key>`).

Pagination params (all optional): `?q=<substr>&status=<filter>&page=<n>&page_size=<n>`.
- Without any params, the response is the full unsorted list (legacy
  shape; kept for scripts/integrations).
- With any param the server filters by `q` (matches email/name/plan/space,
  case-insensitive), sorts by health (healthy/most-remaining first,
  exhausted/no-workspace/permanent to the bottom), and slices to the
  requested page. `page_size` is clamped to `[1, 500]` (default 50).
- The pool-wide `summary` block is always returned and is computed
  across ALL accounts regardless of `q` — that way the headline cards
  stay stable while the user searches.

Response shape:
```json
{
  "total": 247,
  "available": 130,
  "filtered_total": 12,
  "page": 0,
  "page_size": 20,
  "models": [{ "id": "...", "name": "..." }],
  "summary": {
    "exhausted_only": 4,
    "no_workspace": 2,
    "ai_disabled": 1,
    "premium_accounts": 5,
    "unlimited_accounts": 10,
    "exhausted_trials": 6,
    "research_limited": 0,
    "total_research_usage": 14,
    "total_remaining": 12345,
    "total_space_usage": 1000, "total_space_limit": 4000,
    "total_user_usage": 1100, "total_user_limit": 4000,
    "total_space_remaining": 3000, "total_user_remaining": 2900,
    "total_premium_balance": 500, "total_premium_limit": 1000
  },
  "accounts": [{
    "email": "...",
    "name": "...",
    "plan": "personal",
    "quota_unlimited": false,
    "ai_disabled": false,
    "space": "...",
    "exhausted": false,
    "permanent": false,
    "eligible": true,
    "usage": 103,
    "limit": 200,
    "remaining": 97,
    "checked_at": "2026-03-17T...",
    "disabled": false,
    "personal_instructions_configured": true,
    "personal_instructions_checked_at": "2026-07-18T...",
    "models": [{ "id": "...", "name": "..." }]
  }]
}
```

Personal-instructions batch jobs probe the default Notion Agent binding. The UI
displays configured / missing / failed / unchecked. Persist only the boolean
result, timestamp, and optional error; never persist or render the page ID or
instruction text.

Account cards expose persistent cross-page selection. `GET
/admin/accounts/selection` returns the emails for the active query/status
filter, enabling true select-all without exposing account tokens. Selected
account work uses `/admin/account-batch-jobs`: check, disable, enable, delete,
delete missing-personal-instructions, exhausted-trial cleanup, and live-rechecked
no-workspace cleanup. Jobs default
to concurrency 10 (server cap 20), report live step progress, preserve the last
20 snapshots in `accounts/.account_batch_jobs.json`, restore after browser
refresh, and allow failed-only retry. A service restart marks in-flight work as
interrupted. Manual disable persists as `disabled: true` and must be honored by
every account picker. Personal-instructions cleanup re-checks every candidate;
probe errors must remain untouched.

The batch manager permits one running job globally. A conflicting start returns
HTTP 409 with `active_job`; the frontend must adopt that job and show its
progress rather than starting a duplicate operation or showing a generic error.

Add Account accepts one token per line and validates five accounts concurrently
by default. The modal always offers "import all" and "configured only"; the
latter must be enforced by the server so missing or failed probes never enter
the pool. Registration jobs and quota refresh retain their existing bounded
worker pools.

### Proxy Navigation

- Click account card → `GET /proxy/start?email=<email>` (opens in new tab)
- "Open Best" button → `GET /proxy/start?best=true`
- Both create a `np_session` cookie and redirect to `/ai` (Notion reverse proxy)

### Dashboard Navigation & Version

- `#dashboard` is the default operational overview: pool health, quota
  diagnostics, API traffic, actionable account issues, and key runtime
  settings.
- `#accounts` is the account-pool page: summary, quota diagnostics, actions, search, cards, and pagination.
- `#settings` is the settings/history page: API connection details, proxy, feature toggles, request history, registration jobs, and version details.
- The Go dashboard handler injects `<meta name="app-version">`; render its short form in the header and preserve the full value in the title.
- `index.html` is served with `Cache-Control: no-store`; hashed assets stay immutable.

## Conventions

- All component code lives in `App.tsx` (single-file for now; split when it grows)
- No external UI library — TailwindCSS utility classes only
- Responsive: 5-col summary grid → 2-col on tablet → 1-col on mobile
- Account cards are sorted server-side by health, then workspace plan
  (Enterprise > Business > Team > Plus/Personal/Free), then stable account name. Private V2 credit
  fields are diagnostic-only and never participate in plan detection or ranking.
- Workspaces sharing one Notion login render as one normal full card with a
  floating workspace switcher; the switcher must not stretch neighboring cards.
- Search is debounced 250ms and forwarded to the server as `?q=`; the
  server filters across all accounts on email/name/plan/workspace
- Pagination: 20 cards per page, fetched on demand (`?page=&page_size=`)
- Keyboard shortcut: `/` focuses search, `Escape` blurs
