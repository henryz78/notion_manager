import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import type { DashboardData, AccountInfo, AccountSummary, RefreshStatus, TokenStats } from './types'
import { fetchDashboardData, fetchAccountSelection, openProxy, openBestProxy, checkAuth, login, logout, triggerRefresh, fetchSettings, updateSettings, addAccount, fetchTokenStats, startAccountBatchJob, getAccountBatchJob, listAccountBatchJobs, retryAccountBatchJob } from './api'
import type { SearchSettings, AccountStatusFilter, AccountBatchJob, AccountBatchJobAction } from './api'
import { fmt, formatTokens, getQuotaStatusByUsage, getQuotaPct, avatarColor, avatarLetter, formatCheckedAt, formatTimestampMs, providerDisplay } from './utils'
import { AccountMenu } from './components/AccountMenu'
import { RegisterModal } from './components/RegisterModal'
import { HistoryDrawer } from './components/HistoryDrawer'
import { RequestHistoryDrawer } from './components/RequestHistoryDrawer'
import { IconUserPlus, IconHistory } from './components/Icons'

// --- Icons ---
const IconBarChart = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/>
  </svg>
)
const IconRefresh = () => (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <path d="M21 2v6h-6"/><path d="M21 13a9 9 0 1 1-3-7.7L21 8"/>
  </svg>
)
const IconZap = () => (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/>
  </svg>
)
const IconClock = () => (
  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>
  </svg>
)
const IconFlask = () => (
  <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M10 2v7.31" />
    <path d="M14 9.3V1.99" />
    <path d="M8.5 2h7" />
    <path d="M14 9.3a6.5 6.5 0 1 1-4 0" />
    <path d="M5.52 16h12.96" />
  </svg>
)
const IconActivity = () => (
  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
  </svg>
)
const IconSettings = () => (
  <svg className="w-3.5 h-3.5 text-text-secondary" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
    <circle cx="12" cy="12" r="3" />
  </svg>
)

const IconPlus = () => (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
  </svg>
)
const IconTrash = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
  </svg>
)

// --- Add Account Modal ---

type AccountImportRow = {
  line: number
  status: 'success' | 'error' | 'skipped'
  title: string
  detail: string
}

function AddAccountModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const [token, setToken] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [results, setResults] = useState<AccountImportRow[]>([])
  const [progress, setProgress] = useState({ done: 0, total: 0 })
  const [completed, setCompleted] = useState(false)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  const parsedLines = useMemo(
    () => token
      .split(/\r?\n/)
      .map((value, index) => ({ line: index + 1, token: value.trim() }))
      .filter(item => item.token),
    [token],
  )
  const uniqueTokenCount = useMemo(
    () => new Set(parsedLines.map(item => item.token)).size,
    [parsedLines],
  )
  const successCount = results.filter(item => item.status === 'success').length
  const failureCount = results.filter(item => item.status === 'error').length
  const skippedCount = results.filter(item => item.status === 'skipped').length

  useEffect(() => { inputRef.current?.focus() }, [])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !loading) onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [loading, onClose])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (parsedLines.length === 0 || loading) return

    const firstLineByToken = new Map<string, number>()
    const candidates: Array<{ line: number; token: string }> = []
    const initialResults: AccountImportRow[] = []
    for (const item of parsedLines) {
      const firstLine = firstLineByToken.get(item.token)
      if (firstLine !== undefined) {
        initialResults.push({
          line: item.line,
          status: 'skipped',
          title: `第 ${item.line} 行已跳过`,
          detail: `内容与第 ${firstLine} 行重复`,
        })
        continue
      }
      firstLineByToken.set(item.token, item.line)
      candidates.push(item)
    }

    setLoading(true)
    setError('')
    setCompleted(false)
    setResults(initialResults)
    setProgress({ done: 0, total: candidates.length })

    let added = 0
    let cursor = 0
    let done = 0
    const importConcurrency = Math.min(5, candidates.length)
    const worker = async () => {
      while (cursor < candidates.length) {
        const item = candidates[cursor++]
        let row: AccountImportRow
        try {
          const res = await addAccount(item.token)
          if (res.error || !res.account) {
            row = {
              line: item.line,
              status: 'error',
              title: `第 ${item.line} 行导入失败`,
              detail: res.error || '账号信息为空',
            }
          } else {
            added++
            row = {
              line: item.line,
              status: 'success',
              title: res.account.email || res.account.name || `第 ${item.line} 行`,
              detail: `${res.account.space || '未命名空间'} · ${res.account.plan_type || '未知套餐'}`,
            }
          }
        } catch (err) {
          row = {
            line: item.line,
            status: 'error',
            title: `第 ${item.line} 行导入失败`,
            detail: err instanceof Error ? err.message : '请求失败',
          }
        }
        done++
        setResults(current => [...current, row].sort((a, b) => a.line - b.line))
        setProgress({ done, total: candidates.length })
      }
    }
    await Promise.all(Array.from({ length: importConcurrency }, () => worker()))

    if (added > 0) onSuccess()
    setLoading(false)
    setCompleted(true)
  }

  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm px-4 max-sm:px-0 max-sm:items-end"
      onClick={() => { if (!loading) onClose() }}
    >
      <div className="w-full max-w-xl max-h-[92vh] overflow-auto bg-[#1a1a1a] border border-white/10 rounded-xl shadow-2xl p-6 max-sm:max-h-[96vh] max-sm:rounded-b-none max-sm:p-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <div>
            <h2 className="text-[16px] font-semibold">批量添加 Notion 账号</h2>
            <div className="text-[11px] text-text-muted mt-0.5">一行一个 token_v2，可同时导入多个账号</div>
          </div>
          <button disabled={loading} onClick={onClose} className="text-text-muted hover:text-white bg-transparent border-none cursor-pointer text-lg px-1 disabled:opacity-30">×</button>
        </div>

        <div className="text-[12px] text-text-secondary mb-4 space-y-1.5">
          <p>每行粘贴一个 <code className="bg-white/[.08] px-1 py-0.5 rounded text-[11px]">token_v2</code>，系统默认并行验证 5 个，单个 token 也照常支持。</p>
          <p className="text-text-muted">获取方式：打开 <code className="bg-white/[.08] px-1 py-0.5 rounded text-[11px]">notion.so</code> → F12 → Application → Cookies → 复制 <code className="bg-white/[.08] px-1 py-0.5 rounded text-[11px]">token_v2</code> 的值</p>
        </div>

        <form onSubmit={handleSubmit}>
          <textarea
            ref={inputRef}
            value={token}
            onChange={e => {
              setToken(e.target.value)
              if (completed) {
                setCompleted(false)
                setResults([])
                setProgress({ done: 0, total: 0 })
              }
            }}
            placeholder={'token_v2_账号1\ntoken_v2_账号2\ntoken_v2_账号3'}
            rows={7}
            disabled={loading}
            className="w-full py-2.5 px-3 bg-transparent border border-white/10 rounded-lg text-[13px] text-text-primary outline-none focus:border-white/30 focus:ring-1 focus:ring-white/10 transition-all placeholder:text-white/25 resize-y font-mono disabled:opacity-60"
          />
          <div className="flex items-center justify-between gap-3 mt-1.5 text-[11px] text-text-muted">
            <span>{parsedLines.length} 行有效内容 · {uniqueTokenCount} 个待导入账号</span>
            {loading && <span className="text-notion-blue">正在处理 {progress.done} / {progress.total}</span>}
          </div>
          {error && (
            <div className="text-err text-[12px] mt-2 px-1">{error}</div>
          )}

          {results.length > 0 && (
            <div className="mt-3 border border-white/10 rounded-lg overflow-hidden">
              <div className="flex items-center justify-between gap-3 px-3 py-2 bg-white/[.035] text-[11px]">
                <span className="text-text-secondary">导入结果</span>
                <span className="tabular-nums">
                  <span className="text-ok">成功 {successCount}</span>
                  <span className="text-text-muted"> · </span>
                  <span className={failureCount > 0 ? 'text-err' : 'text-text-muted'}>失败 {failureCount}</span>
                  {skippedCount > 0 && <span className="text-text-muted"> · 跳过 {skippedCount}</span>}
                </span>
              </div>
              <div className="max-h-48 overflow-auto divide-y divide-white/[.05]">
                {results.map(item => (
                  <div key={`${item.line}-${item.status}`} className="px-3 py-2 text-[11px]">
                    <div className="flex items-center gap-2">
                      <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${item.status === 'success' ? 'bg-ok' : item.status === 'error' ? 'bg-err' : 'bg-text-muted'}`} />
                      <span className={item.status === 'error' ? 'text-err' : 'text-text-primary'}>{item.title}</span>
                      <span className="ml-auto text-text-muted tabular-nums">第 {item.line} 行</span>
                    </div>
                    <div className="mt-0.5 ml-3.5 text-text-muted break-all">{item.detail}</div>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="flex gap-2.5 mt-4">
            <button
              type="button"
              onClick={onClose}
              disabled={loading}
              className="flex-1 py-2.5 bg-transparent hover:bg-white/5 text-text-secondary rounded-lg text-[13px] font-medium cursor-pointer transition-colors border border-white/10"
            >
              {completed ? '关闭' : '取消'}
            </button>
            {completed ? (
              <button
                type="button"
                onClick={() => {
                  setToken('')
                  setResults([])
                  setCompleted(false)
                  setProgress({ done: 0, total: 0 })
                  window.setTimeout(() => inputRef.current?.focus(), 0)
                }}
                className="flex-1 py-2.5 bg-white hover:bg-white/90 text-black rounded-lg text-[13px] font-semibold cursor-pointer transition-colors border-none"
              >
                继续添加
              </button>
            ) : (
              <button
                type="submit"
                disabled={loading || uniqueTokenCount === 0}
                className="flex-1 py-2.5 bg-white hover:bg-white/90 text-black rounded-lg text-[13px] font-semibold cursor-pointer transition-colors border-none disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {loading ? `正在导入 ${progress.done}/${progress.total}` : `导入 ${uniqueTokenCount || ''} 个账号`}
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  )
}

// --- Login Page ---

function LoginPage({ onSuccess }: { onSuccess: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => { inputRef.current?.focus() }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!password.trim()) return
    setLoading(true)
    setError('')
    try {
      const result = await login(password)
      if (result.ok) {
        onSuccess()
        return
      }
      setError(result.error || '密码错误')
      setPassword('')
      inputRef.current?.focus()
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录请求失败')
      setPassword('')
      inputRef.current?.focus()
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center mb-8">
          <div className="w-12 h-12 bg-[#1a1a1a] border border-white/10 rounded-xl flex items-center justify-center text-xl font-extrabold text-white mb-4">N</div>
          <h1 className="text-xl font-semibold tracking-tight">notion-manager</h1>
          <p className="text-[13px] text-text-muted mt-1">输入管理密钥以访问 Dashboard</p>
        </div>
        <form onSubmit={handleSubmit}>
          <div className="relative mb-4">
            <input
              ref={inputRef}
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder="管理密钥"
              autoComplete="current-password"
              className="w-full py-2.5 px-4 bg-transparent border border-white/10 rounded-lg text-[14px] text-text-primary outline-none focus:border-white/30 focus:ring-1 focus:ring-white/10 transition-all placeholder:text-white/25"
            />
          </div>
          {error && (
            <div className="text-err text-[12px] mb-3 px-1">{error}</div>
          )}
          <button
            type="submit"
            disabled={loading || !password.trim()}
            className="w-full py-2.5 bg-white hover:bg-white/90 text-black rounded-lg text-[14px] font-semibold cursor-pointer transition-colors border-none disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {loading ? '验证中...' : '登录'}
          </button>
        </form>
      </div>
    </div>
  )
}

// --- Header ---

type DashboardPage = 'accounts' | 'settings'

function displayVersion(version: string): string {
  const value = (version || 'dev').trim()
  return /^[0-9a-f]{12,}$/i.test(value) ? value.slice(0, 7) : value
}

function Header({ query, onQuery, onLogout, authRequired, activePage, onPageChange, version }: {
  query: string
  onQuery: (q: string) => void
  onLogout: () => void
  authRequired: boolean
  activePage: DashboardPage
  onPageChange: (page: DashboardPage) => void
  version: string
}) {
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === '/') {
        // Don't hijack "/" when the user is already typing in another
        // input/textarea/contenteditable — otherwise modals like the
        // register form (proxy URL, credentials textarea, etc.) lose
        // focus mid-keystroke.
        const ae = document.activeElement as HTMLElement | null
        const inEditable =
          !!ae &&
          (ae.tagName === 'INPUT' ||
            ae.tagName === 'TEXTAREA' ||
            ae.tagName === 'SELECT' ||
            ae.isContentEditable)
        if (inEditable) return
        e.preventDefault()
        inputRef.current?.focus()
      }
      if (e.key === 'Escape' && document.activeElement === inputRef.current) {
        inputRef.current?.blur()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [])

  return (
    <header className="sticky top-0 z-50 flex items-center gap-5 px-6 py-2.5 border-b border-border bg-bg-secondary/90 backdrop-blur-xl max-md:flex-wrap max-md:gap-2 max-md:px-3">
      <div className="flex items-center gap-2.5 min-w-0 max-md:flex-1">
        <div className="w-7 h-7 bg-[#333] rounded-md flex items-center justify-center text-sm font-extrabold text-white">N</div>
        <span className="text-[15px] font-semibold tracking-tight">
          notion-manager
          <span className="text-text-secondary font-normal text-[13px] ml-1.5 max-sm:hidden">dashboard</span>
        </span>
        <span
          className="text-[10px] text-text-muted font-mono bg-white/[.04] border border-white/[.06] rounded px-1.5 py-0.5"
          title={`当前运行版本：${version}`}
        >
          {displayVersion(version)}
        </span>
      </div>
      <nav className="flex items-center rounded-lg bg-black/20 p-1 border border-white/[.05] max-md:order-2 max-md:w-full">
        <button
          onClick={() => onPageChange('accounts')}
          className={`px-3 py-1.5 rounded-md text-[12px] font-medium cursor-pointer border-none transition-colors max-md:flex-1 ${activePage === 'accounts' ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
        >
          账号管理
        </button>
        <button
          onClick={() => onPageChange('settings')}
          className={`px-3 py-1.5 rounded-md text-[12px] font-medium cursor-pointer border-none transition-colors max-md:flex-1 ${activePage === 'settings' ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
        >
          设置与记录
        </button>
      </nav>
      <div className="flex items-center gap-3 ml-auto max-md:order-3 max-md:ml-0 max-md:w-full">
        {activePage === 'accounts' && <div className="relative w-72 max-md:flex-1">
          <svg className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" />
          </svg>
          <input
            ref={inputRef}
            value={query}
            onChange={e => onQuery(e.target.value)}
            placeholder="搜索账号、邮箱、计划..."
            className="w-full py-1.5 pl-8 pr-10 bg-bg-input border border-border rounded-md text-[13px] text-text-primary outline-none focus:border-white/20 transition-colors placeholder:text-text-muted"
          />
          <kbd className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[11px] text-text-muted bg-bg-card border border-border rounded px-1.5 py-0.5">/</kbd>
        </div>}
        {authRequired && (
          <button
            onClick={onLogout}
            className="text-[12px] text-text-secondary hover:text-text-primary cursor-pointer transition-colors bg-transparent border-none px-2 py-1"
            title="退出登录"
          >
            退出
          </button>
        )}
      </div>
    </header>
  )
}

function StatCard({ label, value, sub, color, icon }: { label: string; value: string | number; sub: string; color?: string; icon?: React.ReactNode }) {
  return (
    <div className="px-6 py-5 max-sm:px-3 max-sm:py-3">
      <div className="text-[11px] text-text-secondary uppercase tracking-wider mb-1 flex items-center gap-1.5">
        {icon}
        <span>{label}</span>
      </div>
      <div className="text-2xl font-bold tracking-tight tabular-nums max-sm:text-xl" style={color ? { color } : undefined}>{value}</div>
      <div className="text-[11px] text-text-muted mt-1 truncate">{sub}</div>
    </div>
  )
}

function hasPremiumAccess(account: AccountInfo): boolean {
  return !!account.has_premium || (account.premium_limit || 0) > 0 || (account.premium_balance || 0) > 0
}

function planIncludesFullNotionAI(plan: string): boolean {
  const normalized = (plan || '').trim().toLowerCase()
  return normalized === 'business' || normalized === 'enterprise'
}

function getSpaceQuota(account: AccountInfo) {
  const usage = account.space_usage ?? account.usage ?? 0
  const limit = account.space_limit ?? account.limit ?? 0
  const remaining = account.space_remaining ?? Math.max(limit - usage, 0)
  return { usage, limit, remaining }
}

function getUserQuota(account: AccountInfo) {
  const usage = account.user_usage ?? 0
  const limit = account.user_limit ?? 0
  const remaining = account.user_remaining ?? Math.max(limit - usage, 0)
  return { usage, limit, remaining }
}

function isSameQuota(a: { usage: number; limit: number }, b: { usage: number; limit: number }): boolean {
  return a.limit > 0 && a.limit === b.limit && a.usage === b.usage
}

function mergeQuotaStatus(statuses: Array<'ok' | 'low' | 'exhausted'>): 'ok' | 'low' | 'exhausted' {
  if (statuses.includes('exhausted')) return 'exhausted'
  if (statuses.includes('low')) return 'low'
  return 'ok'
}

function OverviewBar({ label, usage, limit }: { label: string; usage: number; limit: number }) {
  const pct = getQuotaPct(usage, limit)
  const remaining = Math.max(limit - usage, 0)
  const status = getQuotaStatusByUsage(usage, limit)
  const fillClass = status === 'exhausted' ? 'bg-err opacity-40'
    : status === 'low' ? 'bg-warn' : 'bg-ok'
  const numColor = status === 'exhausted' ? 'text-err'
    : status === 'low' ? 'text-warn' : 'text-text-primary'

  return (
    <div>
      <div className="flex justify-between items-center mb-1.5">
        <span className="text-[10px] text-text-muted uppercase tracking-wider">{label}</span>
        <span className={`text-[11px] font-semibold tabular-nums ${numColor}`}>
          {fmt(remaining)} <span className="text-text-muted font-normal">/ {fmt(limit)} 剩余</span>
        </span>
      </div>
      <div className="h-[2px] bg-white/[.06] rounded-full overflow-hidden">
        <div className={`h-full rounded-full transition-all duration-500 ${fillClass}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function TotalQuotaBar({ summary }: { summary?: AccountSummary | null }) {
  const totalSpaceUsage = summary?.total_space_usage ?? 0
  const totalSpaceLimit = summary?.total_space_limit ?? 0
  const totalUserUsage = summary?.total_user_usage ?? 0
  const totalUserLimit = summary?.total_user_limit ?? 0
  const totalPremiumBalance = summary?.total_premium_balance ?? 0
  const totalPremiumLimit = summary?.total_premium_limit ?? 0
  const sameBasicQuota = isSameQuota(
    { usage: totalSpaceUsage, limit: totalSpaceLimit },
    { usage: totalUserUsage, limit: totalUserLimit },
  )

  return (
    <div className="mb-5 space-y-3">
      <div className="flex justify-between items-center">
        <span className="text-[11px] text-text-secondary uppercase tracking-wider flex items-center gap-1.5"><IconBarChart /> Notion 私有接口计数（诊断）</span>
        {totalPremiumLimit > 0 && (
          <span className="text-[12px] text-text-muted tabular-nums">
            Premium balance <span className="text-[#7eb8ff] font-semibold">{fmt(totalPremiumBalance)}</span> · monthly limit {fmt(totalPremiumLimit)}
          </span>
        )}
      </div>
      {sameBasicQuota ? (
        <OverviewBar label="Basic raw" usage={totalSpaceUsage} limit={totalSpaceLimit} />
      ) : (
        <>
          <OverviewBar label="Space raw" usage={totalSpaceUsage} limit={totalSpaceLimit} />
          <OverviewBar label="User raw" usage={totalUserUsage} limit={totalUserLimit} />
        </>
      )}
      <div className="text-[10px] text-text-muted leading-relaxed">
        以上是 Notion 私有接口返回的诊断计数，官网未公开字段定义；Custom Agents 的 Notion credits 不在这里。
      </div>
    </div>
  )
}

function QuotaBar({ label, labelClass, usage, limit, status }: { label: string; labelClass?: string; usage?: number; limit?: number; status?: 'ok' | 'low' | 'exhausted' }) {
  const pct = getQuotaPct(usage, limit)
  const resolvedStatus = status || getQuotaStatusByUsage(usage, limit)
  const fillClass = resolvedStatus === 'exhausted' ? 'bg-err opacity-40'
    : resolvedStatus === 'low' ? 'bg-warn' : 'bg-ok'
  const numColor = resolvedStatus === 'exhausted' ? 'text-err'
    : resolvedStatus === 'low' ? 'text-warn' : 'text-text-primary'

  return (
    <div className="mb-1.5">
      <div className="flex justify-between items-baseline mb-1">
        <span className={`text-[10px] ${labelClass || 'text-text-muted'}`}>{label}</span>
        <span className={`text-[11px] font-semibold tabular-nums ${numColor}`}>
          {fmt(usage || 0)} <span className="text-text-muted font-normal">/</span> {fmt(limit || 0)}
        </span>
      </div>
      <div className="h-[2px] bg-white/[.06] rounded-full overflow-hidden">
        <div className={`h-full rounded-full transition-all duration-500 ${fillClass}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function Badge({ children, variant }: { children: React.ReactNode; variant: 'plan' | 'premium' | 'research' | 'warning' | 'model' | 'ok' }) {
  const cls: Record<string, string> = {
    plan: 'text-text-secondary',
    premium: 'text-[#7eb8ff]',
    research: 'text-research',
    warning: 'text-red-400 bg-red-500/10 px-1.5 rounded',
    model: 'text-text-secondary hover:text-white transition-colors cursor-pointer',
    ok: 'text-ok bg-ok/10 px-1.5 rounded',
  }
  return (
    <span className={`inline-flex items-center gap-1.5 py-0.5 text-[11px] font-medium whitespace-nowrap ${cls[variant] || ''}`}>
      {children}
    </span>
  )
}

function AccountCard({
  account,
  onChanged,
  selected,
  onToggleSelected,
}: {
  account: AccountInfo
  onChanged: () => void
  selected: boolean
  onToggleSelected: () => void
}) {
  const [showModels, setShowModels] = useState(false)
  const spaceQuota = getSpaceQuota(account)
  const userQuota = getUserQuota(account)
  const sameBasicQuota = isSameQuota(spaceQuota, userQuota)
  const premium = hasPremiumAccess(account)
  const fullNotionAI = planIncludesFullNotionAI(account.plan)
  const noWorkspace = !!account.no_workspace
  const authInvalid = !!account.auth_invalid
  const manuallyDisabled = !!account.disabled
  const temporarilyUnavailable = !authInvalid && !!account.temporarily_unavailable
  const status = manuallyDisabled || account.permanent || account.exhausted || noWorkspace || authInvalid || temporarilyUnavailable
    ? 'exhausted'
    : mergeQuotaStatus([
      getQuotaStatusByUsage(spaceQuota.usage, spaceQuota.limit),
      getQuotaStatusByUsage(userQuota.usage, userQuota.limit),
    ])
  const modelCount = account.models?.length || 0

  const dotCls = status === 'exhausted' ? 'bg-err' : status === 'low' ? 'bg-err' : 'bg-ok'
  // no_workspace shares the exhausted card style so the operator
  // immediately sees the account is unhealthy. Click-through is blocked
  // because Notion's /ai SPA hangs indefinitely on these accounts (the
  // root-cause this fix is for).
  const cardBg = account.permanent ? 'bg-bg-exhausted border-white/[0.03] opacity-55'
    : manuallyDisabled || account.exhausted || noWorkspace || authInvalid || temporarilyUnavailable ? 'bg-bg-exhausted border-white/[0.03]'
    : 'bg-bg-card hover:bg-bg-card-hover border-white/[0.03] hover:border-white/[0.07]'

  const handleClick = () => {
    if (manuallyDisabled) {
      alert('该账号已被手动禁用。重新启用后才会参与网关请求和代理选号。')
      return
    }
    if (authInvalid) {
      alert('该账号 Cookie/token 已失效，请重新导入账号。')
      return
    }
    if (noWorkspace) {
      // Use a native alert — we don't have a toast infra and openProxy
      // would otherwise pop a tab that displays raw JSON 409 to the user.
      alert('该账号没有可访问的 Notion 工作区，无法打开 /ai 反代。请重新注册或选择其他账号。')
      return
    }
    if (temporarilyUnavailable) {
      alert(`该账号刚刚请求失败，已临时跳过。原因：${account.last_failure_reason || 'temporary_failure'}`)
      return
    }
    openProxy(account.email)
  }

  return (
    <div
      className={`rounded-lg p-4 border max-sm:p-3 ${manuallyDisabled || authInvalid || noWorkspace || temporarilyUnavailable ? 'cursor-not-allowed' : 'cursor-pointer hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/30'} transition-all duration-200 ${selected ? 'ring-1 ring-notion-blue border-notion-blue/60' : ''} ${cardBg}`}
      onClick={handleClick}
      title={manuallyDisabled ? '账号已被手动禁用，不参与网关请求和代理选号' : authInvalid ? '账号 Cookie/token 已失效，请重新导入账号' : noWorkspace ? '账号无可访问工作区，已被排除出选号池' : temporarilyUnavailable ? `临时跳过：${account.last_failure_reason || 'temporary_failure'}` : undefined}
    >
      {/* Header */}
      <div className="flex items-center gap-2.5 mb-2.5">
        <input
          type="checkbox"
          checked={selected}
          onClick={(e) => e.stopPropagation()}
          onChange={(e) => {
            e.stopPropagation()
            onToggleSelected()
          }}
          className="w-4 h-4 shrink-0 accent-notion-blue cursor-pointer"
          aria-label={`选择账号 ${account.email}`}
          title="选择账号进行批量操作"
        />
        <div
          className="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold text-white shrink-0"
          style={{ background: avatarColor(account.name) }}
        >
          {avatarLetter(account.name)}
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-semibold truncate">
            {account.name || 'Unknown'}
            {account.space && <span className="text-text-secondary font-normal"> · {account.space}</span>}
          </div>
          <div className="text-[11px] text-text-secondary truncate">{account.email || '—'}</div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <div className={`w-2 h-2 rounded-full ${dotCls}`} />
          <AccountMenu account={account} onChanged={onChanged} />
        </div>
      </div>

      {/* Badges */}
      <div className="flex gap-3 flex-wrap mt-3 mb-2.5 items-center">
        <Badge variant="plan">{account.plan || 'unknown'}</Badge>
        <Badge variant={fullNotionAI ? 'premium' : 'plan'}>
          {fullNotionAI ? 'Notion AI 已包含' : 'AI 有限试用'}
        </Badge>
        {account.registered_via && (
          <Badge variant="plan">via {providerDisplay(account.registered_via)}</Badge>
        )}
        <span title={
          account.personal_instructions_check_error
            ? account.personal_instructions_check_error
            : account.personal_instructions_checked_at
              ? `检测于 ${new Date(account.personal_instructions_checked_at).toLocaleString('zh-CN', { hour12: false })}`
              : '尚未批量检测'
        }>
          {account.personal_instructions_check_error ? (
            <Badge variant="warning">个人指令检测失败</Badge>
          ) : account.personal_instructions_configured === true ? (
            <Badge variant="ok">官网个人指令已设置</Badge>
          ) : account.personal_instructions_configured === false ? (
            <Badge variant="plan">官网个人指令未设置</Badge>
          ) : (
            <Badge variant="plan">个人指令未检测</Badge>
          )}
        </span>
        {premium && <Badge variant="premium">Premium 接口信号</Badge>}
        {(account.research_usage != null && account.research_usage > 0) && (
          <Badge variant="research">
            <IconFlask /> Research 用量 {account.research_usage}（接口值）
          </Badge>
        )}
        {account.exhausted && !account.permanent && <Badge variant="warning">AI 当前不可用</Badge>}
        {account.permanent && <Badge variant="warning">AI 试用已用完</Badge>}
        {manuallyDisabled && <Badge variant="warning">已手动禁用</Badge>}
        {authInvalid && <Badge variant="warning">Cookie 失效</Badge>}
        {noWorkspace && <Badge variant="warning">无工作区</Badge>}
        {temporarilyUnavailable && (
          <Badge variant="warning">临时跳过 {account.last_failure_reason || 'failure'}</Badge>
        )}
        {modelCount > 0 && (
          <button
            onClick={e => { e.stopPropagation(); setShowModels(!showModels) }}
            className="cursor-pointer border-none bg-transparent p-0 text-[11px] text-text-secondary hover:text-white transition-colors"
          >
            {modelCount} models {showModels ? '▴' : '▾'}
          </button>
        )}
      </div>

      {/* Quotas */}
      {sameBasicQuota ? (
        <QuotaBar label="Basic raw" usage={spaceQuota.usage} limit={spaceQuota.limit} />
      ) : (
        <>
          <QuotaBar label="Space raw" usage={spaceQuota.usage} limit={spaceQuota.limit} />
          {userQuota.limit > 0 && <QuotaBar label="User raw" usage={userQuota.usage} limit={userQuota.limit} />}
        </>
      )}
      {premium && <QuotaBar label="Premium monthlyAllocated raw" labelClass="text-[#7eb8ff]" usage={account.premium_usage} limit={account.premium_limit} />}
      <div className="flex flex-wrap gap-3 mt-2 text-[10px] text-text-muted">
        <span>Basic 估算余量 {fmt(account.remaining || 0)}</span>
        {premium && <span>Premium balance 原始值 {fmt(account.premium_balance || 0)}</span>}
      </div>

      {/* Models (expandable) */}
      {showModels && account.models && account.models.length > 0 && (
        <div className="flex flex-wrap gap-1 mt-1.5 mb-1">
          {account.models.map(m => (
            <span key={m.id} className="text-[10px] px-1.5 py-0.5 bg-white/[.06] rounded text-text-secondary">
              {m.name || m.id}
            </span>
          ))}
        </div>
      )}

      {/* Footer */}
      <div className="flex justify-between items-center mt-2 pt-2 border-t border-border">
        <span className="text-[10px] text-text-muted flex items-center gap-1 min-w-0">
          <IconClock />
          <span className="truncate">检查 {formatCheckedAt(account.checked_at)} · 最近 AI {formatTimestampMs(account.last_usage_at)}</span>
        </span>
        {manuallyDisabled ? (
          <span className="text-[11px] text-err font-medium">已禁用</span>
        ) : noWorkspace ? (
          <span className="text-[11px] text-err font-medium">不可用 ⚠</span>
        ) : (
          <span className="text-[11px] text-text-secondary hover:text-white font-medium transition-colors">打开代理 →</span>
        )}
      </div>
    </div>
  )
}

const accountBatchActionLabels: Record<AccountBatchJobAction, string> = {
  check_personal_instructions: '检测官网个人指令',
  disable: '批量禁用账号',
  enable: '批量启用账号',
  delete: '批量删除账号',
  delete_missing_personal_instructions: '检测并删除未设置个人指令账号',
  delete_exhausted: '清理已用完试用额度账号',
}

const accountStatusFilterOptions: Array<{ value: AccountStatusFilter; label: string }> = [
  { value: 'all', label: '全部账号' },
  { value: 'available', label: '可用账号' },
  { value: 'disabled', label: '手动禁用' },
  { value: 'exhausted', label: '额度用完' },
  { value: 'auth_invalid', label: 'Cookie 失效' },
  { value: 'no_workspace', label: '无工作区' },
  { value: 'temporarily_unavailable', label: '临时跳过' },
  { value: 'personal_configured', label: '个人指令已设置' },
  { value: 'personal_missing', label: '个人指令未设置' },
  { value: 'personal_failed', label: '个人指令检测失败' },
  { value: 'personal_unchecked', label: '个人指令未检测' },
]

function AccountBatchProgress({
  job,
  retrying,
  onRetry,
  onClose,
}: {
  job: AccountBatchJob
  retrying: boolean
  onRetry: () => void
  onClose: () => void
}) {
  const pct = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : 0
  const activeSteps = job.steps.filter(step => step.status === 'running').slice(0, 3)
  const failedSteps = job.steps.filter(step => step.status === 'failed').slice(0, 3)
  const isPersonal = job.action === 'check_personal_instructions' || job.action === 'delete_missing_personal_instructions'
  return (
    <div className="mb-4 rounded-lg border border-notion-blue/35 bg-notion-blue/[.07] p-3.5">
      <div className="flex items-start gap-3 max-sm:flex-col">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[13px] font-semibold text-text-primary">{accountBatchActionLabels[job.action]}</span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded ${job.state === 'running' ? 'text-notion-blue bg-notion-blue/10' : job.failed > 0 ? 'text-warn bg-warn/10' : 'text-ok bg-ok/10'}`}>
              {job.state === 'running' ? '并行处理中' : job.state === 'interrupted' ? '任务中断' : '已完成'}
            </span>
            <span className="text-[10px] text-text-muted">并发 {job.concurrency}</span>
          </div>
          <div className="mt-2 flex items-center gap-3 text-[11px] text-text-secondary flex-wrap">
            <span>进度 <strong className="text-text-primary tabular-nums">{job.done} / {job.total}</strong></span>
            <span className="text-ok">成功 {job.succeeded}</span>
            {job.skipped > 0 && <span className="text-text-muted">保留 {job.skipped}</span>}
            {job.failed > 0 && <span className="text-err">失败 {job.failed}</span>}
            {isPersonal && <><span>已设置 {job.configured}</span><span>未设置 {job.missing}</span></>}
          </div>
          <div className="mt-2 h-1.5 bg-white/[.07] rounded-full overflow-hidden">
            <div className="h-full bg-notion-blue rounded-full transition-all duration-300" style={{ width: `${pct}%` }} />
          </div>
          {activeSteps.length > 0 && (
            <div className="mt-2 text-[10px] text-text-muted truncate">
              正在处理：{activeSteps.map(step => step.email).join('、')}
            </div>
          )}
          {job.state !== 'running' && failedSteps.length > 0 && (
            <div className="mt-2 text-[10px] text-err space-y-0.5">
              {failedSteps.map(step => <div key={step.email} className="truncate" title={step.message}>{step.email}：{step.message || '处理失败'}</div>)}
              {job.failed > failedSteps.length && <div>另有 {job.failed - failedSteps.length} 个失败账号</div>}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0 max-sm:w-full max-sm:grid max-sm:grid-cols-2">
          {job.state !== 'running' && job.failed > 0 && (
            <button onClick={onRetry} disabled={retrying} className="px-3 py-1.5 bg-warn/10 hover:bg-warn/20 text-warn rounded-md text-[12px] cursor-pointer border border-warn/25 disabled:opacity-40">
              {retrying ? '正在重试...' : `重试失败项（${job.failed}）`}
            </button>
          )}
          {job.state !== 'running' && (
            <button onClick={onClose} className="px-3 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer border border-border">
              关闭
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

export default function App() {
  const [authState, setAuthState] = useState<'checking' | 'login' | 'authenticated'>('checking')
  const [authRequired, setAuthRequired] = useState(false)
  const [data, setData] = useState<DashboardData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [quotaRefreshing, setQuotaRefreshing] = useState(false)
  const [batchStartingAction, setBatchStartingAction] = useState<AccountBatchJobAction | null>(null)
  const [activeBatchJob, setActiveBatchJob] = useState<AccountBatchJob | null>(null)
  const [selectingAllResults, setSelectingAllResults] = useState(false)
  const [selectedEmails, setSelectedEmails] = useState<Set<string>>(() => new Set())
  const [refreshStatus, setRefreshStatus] = useState<RefreshStatus | null>(null)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<AccountStatusFilter>('all')
  const [refreshTime, setRefreshTime] = useState('')
  const [page, setPage] = useState(0)
  const [settings, setSettings] = useState<SearchSettings | null>(null)
  const [tokenStats, setTokenStats] = useState<TokenStats | null>(null)
  const [apiKeyRevealed, setApiKeyRevealed] = useState(false)
  const [registerOpen, setRegisterOpen] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [requestHistoryOpen, setRequestHistoryOpen] = useState(false)
  const [copiedField, setCopiedField] = useState<'key' | 'base' | null>(null)
  const [showAddModal, setShowAddModal] = useState(false)
  const [activePage, setActivePage] = useState<DashboardPage>(() => window.location.hash === '#settings' ? 'settings' : 'accounts')
  const appVersion = document.querySelector('meta[name="app-version"]')?.getAttribute('content') || 'dev'
  const copyToClipboard = (text: string, field: 'key' | 'base') => {
    navigator.clipboard.writeText(text)
    setCopiedField(field)
    setTimeout(() => setCopiedField(null), 1000)
  }
  // Local draft for the global Notion proxy input. Kept separate from
  // settings.notion_proxy so the user can type without each keystroke
  // hitting the API; we commit on blur/Enter and roll back on error.
  const [proxyDraft, setProxyDraft] = useState('')
  const [proxyError, setProxyError] = useState<string | null>(null)
  const [proxySaving, setProxySaving] = useState(false)
  const [promptModeSaving, setPromptModeSaving] = useState(false)
  const PAGE_SIZE = 20
  const completedBatchJobsRef = useRef<Set<string>>(new Set())

  // Debounced query: typing in the search box shouldn't fire a request
  // on every keystroke; we wait 250ms after the user stops typing and
  // only then re-fetch. The debounced value is what actually goes to
  // the server.
  const [debouncedQuery, setDebouncedQuery] = useState('')
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedQuery(query.trim()), 250)
    return () => clearTimeout(handle)
  }, [query])

  useEffect(() => {
    const syncPageFromHash = () => setActivePage(window.location.hash === '#settings' ? 'settings' : 'accounts')
    window.addEventListener('hashchange', syncPageFromHash)
    return () => window.removeEventListener('hashchange', syncPageFromHash)
  }, [])

  const changePage = (nextPage: DashboardPage) => {
    setActivePage(nextPage)
    const nextHash = nextPage === 'settings' ? '#settings' : '#accounts'
    if (window.location.hash !== nextHash) window.history.replaceState(null, '', nextHash)
  }

  // Check auth on mount
  useEffect(() => {
    checkAuth().then(status => {
      setAuthRequired(status.required)
      if (!status.required || status.authenticated) {
        setAuthState('authenticated')
      } else {
        setAuthState('login')
        setLoading(false)
      }
    }).catch(() => {
      setAuthState('authenticated') // fallback: skip auth
    })
  }, [])

  // loadData fetches the *paginated* account list using the current
  // page + debounced query. The server filters/sorts/slices for us, so
  // `data.accounts` is already the visible page.
  const loadData = useCallback(async () => {
    try {
      const d = await fetchDashboardData({ page, pageSize: PAGE_SIZE, query: debouncedQuery, status: statusFilter })
      setData(d)
      setError(null)
      setRefreshTime(new Date().toLocaleTimeString('zh-CN'))
      if (d.refresh) {
        setRefreshStatus(d.refresh)
      }
    } catch (e: any) {
      setError(e.message || 'Unknown error')
    } finally {
      setLoading(false)
    }
  }, [page, debouncedQuery, statusFilter])

  useEffect(() => {
    if (authState === 'authenticated') loadData()
  }, [authState, loadData])

  // Settings + token stats are pool-wide and don't change with the
  // current page/query, so we only fetch them on auth — not on every
  // page navigation.
  useEffect(() => {
    if (authState !== 'authenticated') return
    fetchSettings()
      .then(s => {
        setSettings(s)
        setProxyDraft(s.notion_proxy ?? '')
      })
      .catch(() => {})
    fetchTokenStats().then(setTokenStats).catch(() => {})
  }, [authState])

  // Restore the active account-batch task after a browser refresh. The server
  // keeps the task and its step progress, so closing/reloading the page does
  // not restart the work.
  useEffect(() => {
    if (authState !== 'authenticated') return
    const storedID = window.localStorage.getItem('notion-manager-active-account-batch-job')
    const restore = async () => {
      try {
        if (storedID) {
          const job = await getAccountBatchJob(storedID)
          setActiveBatchJob(job)
          return
        }
        const jobs = await listAccountBatchJobs()
        const running = jobs.find(job => job.state === 'running')
        if (running) {
          setActiveBatchJob(running)
          window.localStorage.setItem('notion-manager-active-account-batch-job', running.id)
        }
      } catch {
        window.localStorage.removeItem('notion-manager-active-account-batch-job')
      }
    }
    restore()
  }, [authState])

  useEffect(() => {
    if (!activeBatchJob || activeBatchJob.state !== 'running') return
    const poll = async () => {
      try {
        setActiveBatchJob(await getAccountBatchJob(activeBatchJob.id))
      } catch { /* next poll retries */ }
    }
    const interval = window.setInterval(poll, 800)
    return () => window.clearInterval(interval)
  }, [activeBatchJob?.id, activeBatchJob?.state])

  useEffect(() => {
    if (!activeBatchJob || activeBatchJob.state === 'running' || completedBatchJobsRef.current.has(activeBatchJob.id)) return
    completedBatchJobsRef.current.add(activeBatchJob.id)
    window.localStorage.removeItem('notion-manager-active-account-batch-job')
    if (['delete', 'delete_missing_personal_instructions', 'delete_exhausted'].includes(activeBatchJob.action)) {
      setSelectedEmails(new Set())
    }
    loadData()
  }, [activeBatchJob, loadData])

  const handleLogout = async () => {
    await logout()
    setAuthState('login')
    setData(null)
  }

  const refresh = async () => {
    setRefreshing(true)
    await loadData()
    setRefreshing(false)
  }

  const handleQuotaRefresh = async () => {
    setQuotaRefreshing(true)
    try {
      await triggerRefresh()
      // Start polling immediately
      setRefreshStatus(prev => prev ? { ...prev, refreshing: true, done: 0 } : { refreshing: true, done: 0, total: 0 })
    } catch { /* ignore */ }
    setQuotaRefreshing(false)
  }

  const launchAccountBatch = async (action: AccountBatchJobAction, emails: string[]) => {
    if (emails.length === 0 || batchStartingAction || activeBatchJob?.state === 'running') return
    setBatchStartingAction(action)
    try {
      const job = await startAccountBatchJob(action, emails, 10)
      setActiveBatchJob(job)
      window.localStorage.setItem('notion-manager-active-account-batch-job', job.id)
    } catch (e: any) {
      window.alert(`启动批量任务失败：${e?.message || '请求失败'}`)
    } finally {
      setBatchStartingAction(null)
    }
  }

  const launchAllPoolBatch = async (action: AccountBatchJobAction) => {
    try {
      await launchAccountBatch(action, await fetchAccountSelection('', 'all'))
    } catch (e: any) {
      window.alert(`读取全部账号失败：${e?.message || '请求失败'}`)
    }
  }

  const handleCheckPersonalInstructions = async () => {
    if ((data?.total ?? 0) === 0) return
    await launchAllPoolBatch('check_personal_instructions')
  }

  const handleDeleteMissingPersonalInstructions = async () => {
    if ((data?.total ?? 0) === 0 || activeBatchJob?.state === 'running') return
    const knownMissing = data?.summary?.personal_instructions_missing ?? 0
    const confirmed = window.confirm(
      `系统会先重新检测全部 ${data?.total ?? 0} 个账号，然后永久删除当前确实没有设置官网默认 Agent 个人指令的账号。\n\n上次检测显示未设置：${knownMissing} 个。\n检测失败的账号不会删除。\n\n确定继续吗？`,
    )
    if (!confirmed) return
    await launchAllPoolBatch('delete_missing_personal_instructions')
  }

  const handleBulkSelected = async (action: Extract<AccountBatchJobAction, 'delete' | 'disable' | 'enable' | 'check_personal_instructions'>) => {
    const emails = Array.from(selectedEmails)
    if (emails.length === 0) return
    if (action === 'delete') {
      const confirmed = window.confirm(
        `将永久删除选中的 ${emails.length} 个账号及其登录文件。\n\n此操作不可撤销，确定继续吗？`,
      )
      if (!confirmed) return
    }
    await launchAccountBatch(action, emails)
  }

  const handleSelectAllResults = async () => {
    if (selectingAllResults) return
    setSelectingAllResults(true)
    try {
      const emails = await fetchAccountSelection(debouncedQuery, statusFilter)
      setSelectedEmails(new Set(emails))
    } catch (e: any) {
      window.alert(`选择全部结果失败：${e?.message || '请求失败'}`)
    } finally {
      setSelectingAllResults(false)
    }
  }

  const copySelectedEmails = async () => {
    const emails = Array.from(selectedEmails)
    if (emails.length === 0) return
    try {
      await navigator.clipboard.writeText(emails.join('\n'))
      window.alert(`已复制 ${emails.length} 个账号邮箱。`)
    } catch {
      window.alert('复制失败，请检查浏览器剪贴板权限。')
    }
  }

  const handleRetryActiveBatch = async () => {
    if (!activeBatchJob || activeBatchJob.failed <= 0 || batchStartingAction) return
    setBatchStartingAction(activeBatchJob.action)
    try {
      const job = await retryAccountBatchJob(activeBatchJob.id)
      completedBatchJobsRef.current.delete(job.id)
      setActiveBatchJob(job)
      window.localStorage.setItem('notion-manager-active-account-batch-job', job.id)
    } catch (e: any) {
      window.alert(`重试失败项失败：${e?.message || '请求失败'}`)
    } finally {
      setBatchStartingAction(null)
    }
  }

  const closeActiveBatch = () => {
    if (activeBatchJob?.state === 'running') return
    setActiveBatchJob(null)
    window.localStorage.removeItem('notion-manager-active-account-batch-job')
  }

  const handleDeleteExhaustedTrials = async () => {
    const count = data?.summary?.exhausted_trials ?? 0
    if (count <= 0 || activeBatchJob?.state === 'running') return
    const confirmed = window.confirm(
      `将永久删除 ${count} 个已用完 AI 试用额度的 Free/Plus 账号及其登录文件。\n\nBusiness、Enterprise、临时故障和 Cookie 失效账号不会删除。建议先刷新配额。\n\n确定继续吗？`,
    )
    if (!confirmed) return
    await launchAllPoolBatch('delete_exhausted')
  }

  const toggleSetting = async (key: 'enable_web_search' | 'enable_workspace_search' | 'ask_mode_default' | 'debug_logging') => {
    if (!settings) return
    const newVal = !settings[key]
    try {
      const updated = await updateSettings({ [key]: newVal })
      setSettings(updated)
    } catch { /* ignore */ }
  }

  const togglePromptSetting = async (key: 'use_client_system_prompt' | 'use_notion_personal_instructions' | 'enable_tool_bridge') => {
    if (!settings || promptModeSaving) return
    setPromptModeSaving(true)
    try {
      const updated = await updateSettings({ [key]: !settings[key] })
      setSettings(updated)
    } finally {
      setPromptModeSaving(false)
    }
  }

  // saveProxy commits the proxy input draft. We skip the round trip when
  // the value is unchanged (typical blur after focus). Backend rejects
  // unsupported schemes with HTTP 400 + JSON error; we surface the
  // message inline and roll the input back to the persisted value so a
  // typo doesn't get silently saved.
  const saveProxy = async () => {
    if (!settings) return
    const next = proxyDraft.trim()
    if (next === (settings.notion_proxy ?? '').trim()) {
      setProxyDraft(settings.notion_proxy ?? '')
      setProxyError(null)
      return
    }
    setProxySaving(true)
    setProxyError(null)
    try {
      const updated = await updateSettings({ notion_proxy: next })
      setSettings(updated)
      setProxyDraft(updated.notion_proxy ?? '')
    } catch (e: any) {
      setProxyError(e?.message || '保存失败')
      setProxyDraft(settings.notion_proxy ?? '')
    } finally {
      setProxySaving(false)
    }
  }

  // Auto-poll when backend is refreshing quotas
  useEffect(() => {
    if (!refreshStatus?.refreshing) return
    const interval = setInterval(async () => {
      await loadData()
    }, 3000)
    return () => clearInterval(interval)
  }, [refreshStatus?.refreshing, loadData])

  // Server-paginated: data.accounts is already the visible page slice
  // (filtered + sorted server-side). filtered_total tells us how many
  // entries match the current query across the whole pool, which is
  // what we need to render pagination controls.
  const accounts = data?.accounts || []
  const paged = accounts
  const filteredTotal = data?.filtered_total ?? data?.total ?? accounts.length
  const totalPages = Math.max(1, Math.ceil(filteredTotal / PAGE_SIZE))
  const pageEmails = paged.map(account => account.email)
  const selectedOnPage = pageEmails.filter(email => selectedEmails.has(email)).length
  const allPageSelected = pageEmails.length > 0 && selectedOnPage === pageEmails.length
  const batchBusy = !!batchStartingAction || activeBatchJob?.state === 'running'

  const toggleSelectedEmail = (email: string) => {
    setSelectedEmails(current => {
      const next = new Set(current)
      if (next.has(email)) next.delete(email)
      else next.add(email)
      return next
    })
  }

  const toggleCurrentPageSelection = () => {
    setSelectedEmails(current => {
      const next = new Set(current)
      if (allPageSelected) pageEmails.forEach(email => next.delete(email))
      else pageEmails.forEach(email => next.add(email))
      return next
    })
  }

  // Reset page when the (debounced) query changes so the user always
  // lands on the first page of new search results.
  useEffect(() => { setPage(0) }, [debouncedQuery, statusFilter])
  // Clamp `page` if the result set shrank below the current page.
  useEffect(() => {
    if (page > 0 && page >= totalPages) setPage(Math.max(0, totalPages - 1))
  }, [page, totalPages])

  const summary = useMemo(() => {
    if (!data) return null
    const s = data.summary
    // Note: backend's AvailableCount already excludes no_workspace, so
    // (total - available) lumps "exhausted" and "no workspace" together.
    // We split them out explicitly for the operator.
    const exhausted = data.total - data.available
    const exhaustedOnly = s?.exhausted_only ?? 0
    const noWorkspace = s?.no_workspace ?? 0
    const authInvalid = s?.auth_invalid ?? 0
    const disabled = s?.disabled ?? 0
    const otherUnavailable = Math.max(0, exhausted - exhaustedOnly - noWorkspace - authInvalid - disabled)
    const availableRate = data.total > 0 ? Math.round((data.available / data.total) * 100) : 0
    const sameBasicQuota = isSameQuota(
      { usage: s?.total_space_usage ?? 0, limit: s?.total_space_limit ?? 0 },
      { usage: s?.total_user_usage ?? 0, limit: s?.total_user_limit ?? 0 },
    )
    return {
      exhausted,
      exhaustedOnly,
      noWorkspace,
      authInvalid,
      disabled,
      otherUnavailable,
      availableRate,
      totalResearchUsage: s?.total_research_usage ?? 0,
      totalRemaining: s?.total_remaining ?? 0,
      totalSpaceRemaining: s?.total_space_remaining ?? 0,
      totalUserRemaining: s?.total_user_remaining ?? 0,
      totalPremiumBalance: s?.total_premium_balance ?? 0,
      totalPremiumLimit: s?.total_premium_limit ?? 0,
      premiumAccounts: s?.premium_accounts ?? 0,
      sameBasicQuota,
    }
  }, [data])

  // Auth checking spinner
  if (authState === 'checking') {
    return (
      <div className="flex items-center justify-center h-screen gap-3 text-text-secondary text-sm">
        <div className="w-4 h-4 border-2 border-border border-t-notion-blue rounded-full animate-spin" />
      </div>
    )
  }

  // Login page
  if (authState === 'login') {
    return <LoginPage onSuccess={() => { setAuthState('authenticated'); setLoading(true) }} />
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-screen gap-3 text-text-secondary text-sm">
        <div className="w-4 h-4 border-2 border-border border-t-notion-blue rounded-full animate-spin" />
        加载账号数据...
      </div>
    )
  }

  if (error && !data) {
    return (
      <div className="flex items-center justify-center h-screen text-err text-sm">
        加载失败: {error}
      </div>
    )
  }

  const accountSummaryParts = summary
    ? [
      `${data!.available} 可用`,
      summary.exhaustedOnly > 0 ? `${summary.exhaustedOnly} 耗尽` : null,
      summary.noWorkspace > 0 ? `${summary.noWorkspace} 无工作区` : null,
      summary.authInvalid > 0 ? `${summary.authInvalid} Cookie 失效` : null,
      summary.disabled > 0 ? `${summary.disabled} 手动禁用` : null,
      summary.otherUnavailable > 0 ? `${summary.otherUnavailable} 临时跳过` : null,
    ].filter(Boolean).join(' / ')
    : ''

  return (
    <div className="min-h-screen">
      <Header
        query={query}
        onQuery={setQuery}
        onLogout={handleLogout}
        authRequired={authRequired}
        activePage={activePage}
        onPageChange={changePage}
        version={appVersion}
      />

      <main className="max-w-[1280px] mx-auto px-6 py-6 max-sm:px-3 max-sm:py-4">
        {/* Summary */}
        {activePage === 'accounts' && summary && (
          <div className="grid grid-cols-5 divide-x divide-white/[.05] mb-6 max-lg:grid-cols-3 max-md:grid-cols-2 max-md:divide-x-0 max-sm:mb-4">
            <StatCard
              label="总账号" value={data!.total}
              sub={accountSummaryParts}
            />
            <StatCard
              label="可用" value={data!.available}
              sub={`占比 ${summary.availableRate}%`}
              color="var(--color-ok)"
            />
            <StatCard
              label="接口 Basic 估算余量" value={fmt(summary.totalRemaining)}
              sub={summary.sameBasicQuota
                ? 'Space / User 返回值一致 · 非官网承诺额度'
                : `Space ${fmt(summary.totalSpaceRemaining)} · User ${fmt(summary.totalUserRemaining)}（接口值）`}
            />
            <StatCard
              label="Premium balance 原始值" value={fmt(summary.totalPremiumBalance)}
              sub={summary.totalPremiumLimit > 0
                ? `${summary.premiumAccounts} 个接口信号 · Research 接口用量 ${summary.totalResearchUsage}`
                : '接口未返回 Premium 数值 · 不代表 Custom Agents credits'}
              color="var(--color-research, #9b51e0)"
            />
            <StatCard
              icon={<IconActivity />}
              label="Token 用量"
              value={formatTokens(tokenStats?.total.total ?? 0)}
              sub={tokenStats
                ? `今日 ${formatTokens(tokenStats.today.total)} · 输入 ${formatTokens(tokenStats.today.input)} · 输出 ${formatTokens(tokenStats.today.output)}`
                : '尚未产生用量'}
              color="var(--color-notion-blue)"
            />
          </div>
        )}

        {/* Total Quota Bar */}
        {activePage === 'accounts' && <TotalQuotaBar summary={data?.summary} />}

        {/* Refresh Status Banner */}
        {activePage === 'accounts' && refreshStatus?.refreshing && (
          <div className="bg-notion-blue/10 border border-notion-blue/20 rounded-lg p-3 mb-5 flex items-center gap-3">
            <div className="w-4 h-4 border-2 border-notion-blue/30 border-t-notion-blue rounded-full animate-spin shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-[13px] font-medium text-[#5c9ce6]">
                正在刷新配额... {refreshStatus.done}/{refreshStatus.total}
              </div>
              <div className="h-1.5 bg-white/[.06] rounded-full overflow-hidden mt-1.5">
                <div
                  className="h-full bg-notion-blue rounded-full transition-all duration-500"
                  style={{ width: `${refreshStatus.total > 0 ? (refreshStatus.done / refreshStatus.total) * 100 : 0}%` }}
                />
              </div>
            </div>
          </div>
        )}

        {/* Actions */}
        {activePage === 'accounts' && <div className="flex items-center gap-2.5 mb-5 flex-wrap max-sm:grid max-sm:grid-cols-2 max-sm:gap-2 max-sm:[&>button]:justify-center max-sm:[&>button]:px-2 max-sm:[&>button]:text-[12px]">
          <button
            onClick={openBestProxy}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-white hover:bg-white/90 text-[#111] rounded-md text-[13px] font-medium cursor-pointer transition-colors border-none"
          >
            <IconZap /> 打开最优账号
          </button>
          <button
            onClick={handleQuotaRefresh}
            disabled={quotaRefreshing || refreshStatus?.refreshing}
            className={`inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed ${refreshStatus?.refreshing ? 'animate-pulse' : ''}`}
          >
            <IconRefresh /> 刷新配额
          </button>
          <button
            onClick={refresh}
            disabled={refreshing}
            className={`inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed ${refreshing ? 'animate-pulse' : ''}`}
          >
            <IconRefresh /> 刷新数据
          </button>
          <button
            onClick={handleCheckPersonalInstructions}
            disabled={batchBusy || (data?.total ?? 0) === 0}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed"
            title="只检测默认 Notion Agent 是否绑定了官网个人指令页面，不读取或保存指令正文"
          >
            <IconActivity /> {batchStartingAction === 'check_personal_instructions' ? '正在启动...' : '检测官网个人指令'}
          </button>
          <button
            onClick={handleDeleteMissingPersonalInstructions}
            disabled={batchBusy || (data?.total ?? 0) === 0}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-err/10 hover:bg-err/20 text-err rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
            title="会先重新检测全部账号，只永久删除当前确实没有设置官网默认 Agent 个人指令的账号"
          >
            <IconTrash /> {batchStartingAction === 'delete_missing_personal_instructions'
              ? '正在启动...'
              : `删除未设置个人指令（${data?.summary?.personal_instructions_missing ?? 0}）`}
          </button>
          <button
            onClick={handleDeleteExhaustedTrials}
            disabled={batchBusy || (data?.summary?.exhausted_trials ?? 0) <= 0 || !!refreshStatus?.refreshing}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-err/10 hover:bg-err/20 text-err rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
            title="仅永久删除因 complimentary AI responses 用完而禁用的 Free/Plus 账号；不会删除其他故障账号"
          >
            <IconTrash /> {batchStartingAction === 'delete_exhausted' ? '正在启动...' : `清理已用完试用账号（${data?.summary?.exhausted_trials ?? 0}）`}
          </button>
          <button
            onClick={() => setShowAddModal(true)}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border"
          >
            <IconPlus /> 添加账号
          </button>
          <button
            onClick={() => setRegisterOpen(true)}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border"
          >
            <IconUserPlus size={13} /> 注册账号
          </button>
          {refreshTime && (
            <span className="text-[11px] text-text-muted max-sm:col-span-2">
              更新于 {refreshTime}
              {refreshStatus?.last_refresh_at && !refreshStatus.refreshing && (
                <> · 配额刷新于 {new Date(refreshStatus.last_refresh_at).toLocaleTimeString('zh-CN')}</>
              )}
            </span>
          )}
        </div>}

        {activePage === 'settings' && (
          <div className="mb-6">
            <div className="mb-5">
              <h2 className="text-[18px] font-semibold text-text-primary">设置与记录</h2>
              <p className="text-[12px] text-text-muted mt-1">集中管理 API、代理、功能开关和运行记录。</p>
            </div>
            <div className="grid grid-cols-3 gap-3 max-md:grid-cols-1">
              <div className="bg-bg-card border border-border rounded-lg p-4">
                <div className="text-[11px] text-text-muted uppercase tracking-wider">当前版本</div>
                <div className="text-[20px] font-semibold font-mono mt-1" title={appVersion}>{displayVersion(appVersion)}</div>
                <div className="text-[11px] text-text-muted mt-1">用于确认 Railway 是否已经更新</div>
              </div>
              <button
                onClick={() => setRequestHistoryOpen(true)}
                className="text-left bg-bg-card hover:bg-bg-card-hover border border-border rounded-lg p-4 cursor-pointer transition-colors"
              >
                <div className="flex items-center gap-2 text-[13px] font-medium"><IconActivity /> 调用记录</div>
                <div className="text-[11px] text-text-muted mt-2">查看最近 100 条模型、账号、耗时和错误记录</div>
              </button>
              <button
                onClick={() => setHistoryOpen(true)}
                className="text-left bg-bg-card hover:bg-bg-card-hover border border-border rounded-lg p-4 cursor-pointer transition-colors"
              >
                <div className="flex items-center gap-2 text-[13px] font-medium"><IconHistory size={13} /> 注册任务</div>
                <div className="text-[11px] text-text-muted mt-2">查看批量注册历史、结果和重试状态</div>
              </button>
            </div>
          </div>
        )}

        {/* API Settings */}
        {activePage === 'settings' && settings && (() => {
          const apiKey = document.querySelector('meta[name="api-key"]')?.getAttribute('content') || ''
          const apiBase = `${window.location.origin}/v1`
          const maskedKey = apiKey ? apiKey.slice(0, 5) + '•'.repeat(Math.max(0, apiKey.length - 9)) + apiKey.slice(-4) : ''
          return (
            <div className="mb-6 px-5 py-5 bg-[#171717] border border-white/5 rounded-lg shadow-inner max-sm:px-3 max-sm:py-4">
              <div className="mb-4">
                <div className="text-[14px] text-text-primary font-semibold flex items-center gap-2"><IconSettings /> API 与功能设置</div>
                <div className="text-[11px] text-text-muted mt-1">修改后立即保存；服务重启后继续生效。</div>
              </div>
              <div className="mb-5 rounded-lg border border-white/[.07] bg-white/[.025] p-3.5">
                <div className="flex items-center justify-between gap-3 mb-3 max-sm:items-start">
                  <div>
                    <div className="text-[13px] font-medium text-text-primary">提示词与工具处理</div>
                    <div className="text-[11px] text-text-muted mt-0.5">三个开关互相独立，可以任意组合；修改后会从新会话开始生效。</div>
                  </div>
                  <span className="text-[10px] text-ok bg-ok/10 border border-ok/20 rounded px-2 py-0.5 shrink-0">
                    {promptModeSaving ? '保存中' : '已保存'}
                  </span>
                </div>
                <div className="grid grid-cols-3 gap-2 max-lg:grid-cols-1">
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.use_client_system_prompt}
                    onClick={() => togglePromptSetting('use_client_system_prompt')}
                    disabled={promptModeSaving}
                    className={`text-left rounded-lg border p-3 cursor-pointer transition-colors disabled:cursor-wait ${settings.use_client_system_prompt ? 'border-notion-blue/60 bg-notion-blue/10' : 'border-border bg-bg-card hover:bg-bg-card-hover'}`}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-[12px] font-semibold text-text-primary">客户端 System Prompt</span>
                      <span className={`text-[10px] ${settings.use_client_system_prompt ? 'text-ok' : 'text-text-muted'}`}>{settings.use_client_system_prompt ? '开启' : '关闭'}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">开启：保留客户端传来的 system prompt。关闭：忽略这部分，但普通问题仍会发送。</div>
                  </button>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.use_notion_personal_instructions}
                    onClick={() => togglePromptSetting('use_notion_personal_instructions')}
                    disabled={promptModeSaving}
                    className={`text-left rounded-lg border p-3 cursor-pointer transition-colors disabled:cursor-wait ${settings.use_notion_personal_instructions ? 'border-notion-blue/60 bg-notion-blue/10' : 'border-border bg-bg-card hover:bg-bg-card-hover'}`}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-[12px] font-semibold text-text-primary">Notion 官网个人指令</span>
                      <span className={`text-[10px] ${settings.use_notion_personal_instructions ? 'text-ok' : 'text-text-muted'}`}>{settings.use_notion_personal_instructions ? '开启' : '关闭'}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">开启：由当前 Notion 账号的默认 Agent 加载官网保存的个人指令，可和左侧同时开启。</div>
                  </button>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={settings.enable_tool_bridge}
                    onClick={() => togglePromptSetting('enable_tool_bridge')}
                    disabled={promptModeSaving}
                    className={`text-left rounded-lg border p-3 cursor-pointer transition-colors disabled:cursor-wait ${settings.enable_tool_bridge ? 'border-notion-blue/60 bg-notion-blue/10' : 'border-border bg-bg-card hover:bg-bg-card-hover'}`}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-[12px] font-semibold text-text-primary">外部工具兼容</span>
                      <span className={`text-[10px] ${settings.enable_tool_bridge ? 'text-ok' : 'text-text-muted'}`}>{settings.enable_tool_bridge ? '开启' : '关闭'}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">开启：让 Claude Code、Tools 和函数调用正常工作。关闭：按普通聊天发送，外部工具调用停用。</div>
                  </button>
                </div>
              </div>
              <div className="flex items-center gap-6 flex-wrap max-sm:flex-col max-sm:items-stretch max-sm:gap-4">
                <div className="flex items-center gap-6 flex-wrap max-sm:flex-col max-sm:items-stretch max-sm:gap-3 max-sm:w-full">
                  <div className="flex items-center gap-1.5 max-sm:flex-wrap">
                    <span className="text-[11px] text-text-muted">API Key</span>
                    <code
                      className={`text-[11px] bg-white/[.05] px-1.5 py-0.5 rounded cursor-pointer hover:bg-white/[.1] transition-colors font-mono max-sm:max-w-[220px] max-sm:truncate ${copiedField === 'key' ? 'text-ok' : 'text-text-primary'}`}
                      onClick={() => copyToClipboard(apiKey, 'key')}
                      title="点击复制"
                    >
                      {copiedField === 'key' ? '✓ 已复制' : (apiKeyRevealed ? apiKey : maskedKey)}
                    </code>
                    <button
                      onClick={() => setApiKeyRevealed(!apiKeyRevealed)}
                      className="ml-3 text-text-muted hover:text-text-primary transition-colors bg-transparent border-none cursor-pointer px-0.5 flex items-center"
                      title={apiKeyRevealed ? '隐藏' : '显示'}
                    >
                      {apiKeyRevealed ? (
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"/><path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"/><line x1="1" y1="1" x2="23" y2="23"/><path d="M14.12 14.12a3 3 0 1 1-4.24-4.24"/>
                        </svg>
                      ) : (
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>
                        </svg>
                      )}
                    </button>
                  </div>
                  <div className="flex items-center gap-1.5 max-sm:flex-wrap">
                    <span className="text-[11px] text-text-muted">Base URL</span>
                    <code
                      className={`text-[11px] bg-white/[.05] px-1.5 py-0.5 rounded cursor-pointer hover:bg-white/[.1] transition-colors font-mono break-all ${copiedField === 'base' ? 'text-ok' : 'text-text-primary'}`}
                      onClick={() => copyToClipboard(apiBase, 'base')}
                      title="点击复制"
                    >
                      {copiedField === 'base' ? '✓ 已复制' : apiBase}
                    </code>
                  </div>
                  <div className="flex items-center gap-1.5 max-sm:grid max-sm:grid-cols-[auto_8px_1fr]">
                    <span className="text-[11px] text-text-muted">全局代理</span>
                    <span
                      className={`inline-block w-1.5 h-1.5 rounded-full ${proxyError ? 'bg-err' : settings.notion_proxy ? 'bg-ok' : 'bg-text-muted/60'}`}
                      title={proxyError ? proxyError : settings.notion_proxy ? '已启用代理' : '直连'}
                    />
                    <input
                      type="text"
                      value={proxyDraft}
                      onChange={e => { setProxyDraft(e.target.value); if (proxyError) setProxyError(null) }}
                      onBlur={saveProxy}
                      onKeyDown={e => {
                        if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                        if (e.key === 'Escape') {
                          setProxyDraft(settings.notion_proxy ?? '')
                          setProxyError(null)
                          ;(e.target as HTMLInputElement).blur()
                        }
                      }}
                      placeholder="留空 = 直连"
                      disabled={proxySaving}
                      className={`text-[11px] bg-white/[.05] px-1.5 py-0.5 rounded font-mono outline-none border w-[160px] focus:w-[280px] transition-[width,border-color] duration-150 max-sm:w-full max-sm:focus:w-full ${proxyError ? 'border-err text-err' : 'border-transparent focus:border-white/20 text-text-primary'} placeholder:text-text-muted/60`}
                      title={proxyError || (settings.notion_proxy ? `当前: ${settings.notion_proxy}` : '当前: 直连')}
                    />
                  </div>
                </div>
                <div className="flex items-center gap-5 ml-auto flex-wrap justify-end max-sm:ml-0 max-sm:grid max-sm:grid-cols-2 max-sm:w-full max-sm:gap-3 max-[360px]:grid-cols-1">
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <button
                      onClick={() => toggleSetting('enable_web_search')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.enable_web_search ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.enable_web_search ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-white font-medium">联网搜索</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <button
                      onClick={() => toggleSetting('enable_workspace_search')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.enable_workspace_search ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.enable_workspace_search ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-text-primary">工作区搜索</span>
                  </label>
                  <label
                    className="flex items-center gap-2 cursor-pointer select-none"
                    title="开启后所有请求默认进入 ASK 模式（仅回答、不写入页面）。单次覆盖：在模型名末尾追加 -ask，例如 claude-sonnet-4.6-ask"
                  >
                    <button
                      onClick={() => toggleSetting('ask_mode_default')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.ask_mode_default ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.ask_mode_default ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-text-primary">ASK 模式</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <button
                      onClick={() => toggleSetting('debug_logging')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.debug_logging ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.debug_logging ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-text-primary">调试日志</span>
                  </label>
                </div>
              </div>
            </div>
          )
        })()}

        {activePage === 'accounts' && <>
        {activeBatchJob && (
          <AccountBatchProgress
            job={activeBatchJob}
            retrying={!!batchStartingAction}
            onRetry={handleRetryActiveBatch}
            onClose={closeActiveBatch}
          />
        )}

        {/* Bulk selection toolbar */}
        <div className="mb-4 rounded-lg border border-border bg-bg-card px-3 py-2.5 flex items-center gap-2.5 flex-wrap max-sm:items-stretch">
          <select
            value={statusFilter}
            onChange={event => setStatusFilter(event.target.value as AccountStatusFilter)}
            disabled={batchBusy}
            className="px-3 py-1.5 bg-bg-secondary text-text-primary rounded-md text-[12px] border border-border outline-none cursor-pointer disabled:opacity-40 max-sm:col-span-2"
            title="按账号状态筛选"
          >
            {accountStatusFilterOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
          </select>
          <button
            onClick={toggleCurrentPageSelection}
            disabled={pageEmails.length === 0 || batchBusy}
            className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-primary rounded-md text-[12px] font-medium cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {allPageSelected ? '取消本页' : '本页全选'}
          </button>
          <button
            onClick={handleSelectAllResults}
            disabled={filteredTotal === 0 || batchBusy || selectingAllResults}
            className="px-3 py-1.5 bg-notion-blue/10 hover:bg-notion-blue/20 text-notion-blue rounded-md text-[12px] font-medium cursor-pointer border border-notion-blue/25 disabled:opacity-40 disabled:cursor-not-allowed"
            title="一次选择当前搜索文字和状态筛选下的全部账号，包括其他页面"
          >
            {selectingAllResults ? '正在全选...' : `全选全部结果（${filteredTotal}）`}
          </button>
          <span className="text-[12px] text-text-secondary mr-auto self-center">
            已选 <strong className="text-text-primary tabular-nums">{selectedEmails.size}</strong> 个
            {selectedOnPage > 0 && selectedEmails.size !== selectedOnPage ? `（本页 ${selectedOnPage} 个）` : ''}
          </span>
          <div className="flex items-center gap-2 flex-wrap max-sm:grid max-sm:grid-cols-2 max-sm:w-full">
            <button
              onClick={copySelectedEmails}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-secondary hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
            >
              复制邮箱
            </button>
            <button
              onClick={() => handleBulkSelected('check_personal_instructions')}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-secondary hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'check_personal_instructions' ? '正在启动...' : '检测所选'}
            </button>
            <button
              onClick={() => handleBulkSelected('disable')}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-warn/10 hover:bg-warn/20 text-warn rounded-md text-[12px] cursor-pointer border border-warn/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'disable' ? '正在启动...' : '禁用所选'}
            </button>
            <button
              onClick={() => handleBulkSelected('enable')}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-ok/10 hover:bg-ok/20 text-ok rounded-md text-[12px] cursor-pointer border border-ok/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'enable' ? '正在启动...' : '启用所选'}
            </button>
            <button
              onClick={() => handleBulkSelected('delete')}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-err/10 hover:bg-err/20 text-err rounded-md text-[12px] cursor-pointer border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'delete' ? '正在启动...' : '删除所选'}
            </button>
            <button
              onClick={() => setSelectedEmails(new Set())}
              disabled={selectedEmails.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-transparent hover:bg-white/[.05] text-text-muted hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-transparent disabled:opacity-40 disabled:cursor-not-allowed"
            >
              清空选择
            </button>
          </div>
        </div>

        {/* Section Title */}
        <div className="text-[12px] font-semibold text-text-secondary uppercase tracking-wider mb-3.5 flex items-center gap-1.5">
          <span>账号池</span>
          <span className="font-normal text-text-muted">({filteredTotal})</span>
        </div>

        {/* Grid */}
        {filteredTotal === 0 ? (
          <div className="text-center py-16 text-text-secondary text-sm">
            没有找到匹配的账号
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(340px,1fr))] gap-2.5 mb-4 max-sm:grid-cols-1">
            {paged.map(acc => (
              <AccountCard
                key={acc.email}
                account={acc}
                onChanged={loadData}
                selected={selectedEmails.has(acc.email)}
                onToggleSelected={() => toggleSelectedEmail(acc.email)}
              />
            ))}
          </div>
        )}

        {/* Pagination */}
        {totalPages > 1 && (
          <div className="flex items-center justify-center gap-2 mb-10">
            <button
              onClick={() => setPage(0)}
              disabled={page === 0}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer transition-colors border border-border disabled:opacity-30 disabled:cursor-not-allowed max-sm:hidden"
            >
              «
            </button>
            <button
              onClick={() => setPage(p => Math.max(0, p - 1))}
              disabled={page === 0}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer transition-colors border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              ‹ 上一页
            </button>
            <span className="text-[12px] text-text-secondary tabular-nums px-3">
              {page + 1} / {totalPages}
            </span>
            <button
              onClick={() => setPage(p => Math.min(totalPages - 1, p + 1))}
              disabled={page >= totalPages - 1}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer transition-colors border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              下一页 ›
            </button>
            <button
              onClick={() => setPage(totalPages - 1)}
              disabled={page >= totalPages - 1}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer transition-colors border border-border disabled:opacity-30 disabled:cursor-not-allowed max-sm:hidden"
            >
              »
            </button>
          </div>
        )}
        </>}
      </main>
      {showAddModal && <AddAccountModal onClose={() => setShowAddModal(false)} onSuccess={loadData} />}

      <RegisterModal
        open={registerOpen}
        onClose={() => setRegisterOpen(false)}
        onJobFinished={() => {
          // Immediate reload so newly-registered accounts show up. The
          // backend kicks off a per-account quota refresh in a goroutine
          // after each success; that lands a few seconds later, so we
          // schedule a second reload to pick up the freshly-cached
          // quota_info from disk.
          loadData()
          window.setTimeout(() => { loadData() }, 4000)
        }}
      />
      <HistoryDrawer
        open={historyOpen}
        onClose={() => setHistoryOpen(false)}
        onRetryStarted={() => {
          // Reload account list once the retry finishes so newly succeeded
          // accounts surface in the dashboard. Best-effort: the drawer's
          // own poller picks up live counters in the meantime.
          window.setTimeout(() => { loadData() }, 4000)
        }}
      />
      <RequestHistoryDrawer
        open={requestHistoryOpen}
        onClose={() => setRequestHistoryOpen(false)}
      />
    </div>
  )
}
