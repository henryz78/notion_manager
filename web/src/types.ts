export interface Model {
  id: string
  name: string
}

export interface AccountInfo {
  email: string
  name: string
  plan: string
  space: string
  exhausted: boolean
  permanent: boolean
  // no_workspace is true when the backend probed loadUserContent and found
  // that user_root.space_views is empty — the /ai SPA gets stuck on a
  // skeleton screen for these accounts. Dashboard treats them as
  // unusable (no click-through, sorted to the bottom, dedicated badge).
  no_workspace?: boolean
  // space_count is the raw probe result. Absent when the backend never
  // probed (fresh registration before the first refresh tick).
  space_count?: number
  workspace_checked_at?: string
  temporarily_unavailable?: boolean
  unavailable_until?: string
  last_failure_reason?: string
  last_failure_at?: string
  auth_invalid?: boolean
  auth_failures?: number
  disabled?: boolean
  eligible?: boolean
  usage?: number
  limit?: number
  remaining?: number
  space_usage?: number
  space_limit?: number
  space_remaining?: number
  user_usage?: number
  user_limit?: number
  user_remaining?: number
  checked_at?: string
  exhausted_at?: string
  last_usage_at?: number
  models?: Model[]
  research_usage?: number
  has_premium?: boolean
  premium_balance?: number
  premium_usage?: number
  premium_limit?: number
  token_v2?: string
  registered_via?: string
  personal_instructions_configured?: boolean
  personal_instructions_checked_at?: string
  personal_instructions_check_error?: string
}

export interface ProviderInfo {
  id: string
  display: string
  format_hint: string
  recommended_concurrency: number
  enabled: boolean
}

export type JobState = 'running' | 'done' | 'cancelled'
export type StepStatus = 'pending' | 'running' | 'ok' | 'fail'

export interface RegisterStep {
  email: string
  status: StepStatus
  message?: string
  space_id?: string
  user_id?: string
  file?: string
  started_at?: number
  ended_at?: number
}

export interface RegisterJob {
  id: string
  created_at: number
  ended_at?: number
  provider?: string
  proxy?: string
  concurrency: number
  total: number
  ok: number
  fail: number
  done: number
  state: JobState
  steps: RegisterStep[]
}

export interface JobStartResponse {
  job_id: string
  provider: string
  total: number
  concurrency: number
  proxy?: string
  retry_of?: string
}

export interface RefreshStatus {
  refreshing: boolean
  done: number
  total: number
  last_refresh_at?: string
  error?: string
}

// AccountSummary mirrors Go's proxy.AccountSummary. The backend computes
// pool-wide aggregates (counts, quota sums) so the dashboard headline
// cards don't need to download the full account list to render. These
// fields reflect ALL accounts regardless of the current ?q= filter, so
// the headline numbers stay stable while the user searches.
export interface AccountSummary {
  exhausted_only: number
  no_workspace: number
  auth_invalid: number
  disabled: number
  premium_accounts: number
  exhausted_trials: number
  personal_instructions_configured: number
  personal_instructions_missing: number
  personal_instructions_failed: number
  personal_instructions_unchecked: number
  research_limited: number // deprecated compatibility field; current backend returns 0
  total_research_usage: number
  total_remaining: number
  total_space_usage: number
  total_space_limit: number
  total_space_remaining: number
  total_user_usage: number
  total_user_limit: number
  total_user_remaining: number
  total_premium_balance: number
  total_premium_limit: number
}

export interface DashboardData {
  total: number
  available: number
  models: Model[]
  accounts: AccountInfo[]
  refresh?: RefreshStatus
  // Present whenever the request used pagination params. `accounts` is
  // already the page slice; use `filtered_total` to compute pagination
  // controls on the client.
  page?: number
  page_size?: number
  filtered_total?: number
  // Pool-wide aggregates over ALL accounts (independent of ?q=).
  // The backend always emits this; the optional marker keeps older
  // dev builds tolerant.
  summary?: AccountSummary
}

export interface TokenBucket {
  input: number
  output: number
  total: number
  requests?: number
}

export interface TokenDayPoint {
  date: string
  input: number
  output: number
  total: number
}

export interface TokenModelRow {
  model: string
  input: number
  output: number
  total: number
  count: number
}

export interface TokenAccountRow {
  email: string
  input: number
  output: number
  total: number
  count: number
}

export interface TokenStats {
  total: TokenBucket
  today: TokenBucket
  last_24h: TokenBucket
  by_day: TokenDayPoint[]
  top_models: TokenModelRow[]
  top_accounts: TokenAccountRow[]
  last_record_at: number
}

export interface APIKeyInfo {
  masked: string
  value?: string
}

export interface DeploymentVersionStatus {
  current_version: string
  latest_version?: string
  status: 'up_to_date' | 'update_available' | 'unknown' | string
  checked_at: string
  published_at?: string
  run_url?: string
  error?: string
}

export type RequestHistoryStatus = 'success' | 'error'
export type RequestHistoryAPI = 'anthropic' | 'openai_chat' | 'openai_responses'
export type RequestPromptMode = 'existing_prompt' | 'notion_personal_instructions' | 'client_and_notion_personal' | 'no_behavior_prompt' | 'not_applicable'

export interface RequestAttempt {
  account_email: string
  outcome?: string
  error?: string
  duration_ms: number
}

// Metadata-only API diagnostics. The backend never includes request messages,
// system prompts, response text, tool arguments, or personal-instruction text.
export interface RequestHistoryEntry {
  id: string
  created_at: string
  api: RequestHistoryAPI | string
  requested_model: string
  used_default_model?: boolean
  notion_model?: string
  account_email?: string
  prompt_mode: RequestPromptMode | string
  tool_count: number
  stream: boolean
  tool_choice?: string
  tool_bridge?: string
  finish_reason?: string
  context_mode?: string
  input_tokens: number
  context_tokens: number
  output_tokens: number
  duration_ms: number
  status: RequestHistoryStatus | string
  http_status: number
  error?: string
  attempts: number
  attempt_details?: RequestAttempt[]
}

export interface RequestHistoryPage {
  total: number
  filtered_total: number
  page: number
  page_size: number
  entries: RequestHistoryEntry[]
}
