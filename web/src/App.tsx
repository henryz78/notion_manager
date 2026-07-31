import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import type { DashboardData, AccountInfo, AccountSummary, DeploymentVersionStatus, RefreshStatus, TokenStats } from './types'
import { fetchAllDashboardData, fetchAccountSelection, openProxy, openBestProxy, checkAuth, login, logout, triggerRefresh, fetchSettings, updateSettings, addAccount, fetchTokenStats, startAccountBatchJob, getAccountBatchJob, listAccountBatchJobs, retryAccountBatchJob, downloadBackup, restoreBackup, fetchAPIKey, fetchVersionStatus } from './api'
import type { SearchSettings, ToolChoicePolicy, AccountStatusFilter, AccountBatchJob, AccountBatchJobAction, AccountImportPersonalInstructionsPolicy } from './api'
import { fmt, formatTokens, getQuotaStatusByUsage, getQuotaPct, avatarColor, avatarLetter, formatCheckedAt, formatTimestampMs, providerDisplay } from './utils'
import { AccountMenu } from './components/AccountMenu'
import { RegisterModal } from './components/RegisterModal'
import { HistoryDrawer } from './components/HistoryDrawer'
import { RequestHistoryDrawer } from './components/RequestHistoryDrawer'
import { IconUserPlus, IconHistory, IconDatabase, IconDownload, IconUpload } from './components/Icons'
import { LanguageToggle } from './components/LanguageToggle'
import { useTranslation } from 'react-i18next'

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
const IconDashboard = () => (
  <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <rect x="3" y="3" width="7" height="7" rx="1" />
    <rect x="14" y="3" width="7" height="7" rx="1" />
    <rect x="3" y="14" width="7" height="7" rx="1" />
    <rect x="14" y="14" width="7" height="7" rx="1" />
  </svg>
)
const IconArrowRight = () => (
  <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M5 12h14" /><path d="m13 6 6 6-6 6" />
  </svg>
)
const IconShield = () => (
  <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10" /><path d="m9 12 2 2 4-4" />
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

function AddAccountModal({
  onClose,
  onSuccess,
}: {
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation()
  const [token, setToken] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [results, setResults] = useState<AccountImportRow[]>([])
  const [progress, setProgress] = useState({ done: 0, total: 0 })
  const [completed, setCompleted] = useState(false)
  const [personalInstructionsPolicy, setPersonalInstructionsPolicy] = useState<AccountImportPersonalInstructionsPolicy>('all')
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
          title: t('modal.add.duplicate_title', { line: item.line }),
          detail: t('modal.add.duplicate_detail', { first: firstLine }),
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
          const res = await addAccount(item.token, personalInstructionsPolicy)
          if (res.error || !res.account) {
            row = {
              line: item.line,
              status: 'error',
              title: t('modal.add.failed_title', { line: item.line }),
              detail: res.error || t('modal.add.empty_account'),
            }
          } else if (res.status === 'skipped' && res.reason === 'duplicate_account') {
            row = {
              line: item.line,
              status: 'skipped',
              title: res.account.email || res.account.name || t('modal.add.duplicate_title', { line: item.line }),
              detail: t('modal.add.duplicate_existing'),
            }
          } else if (res.status === 'skipped' && res.reason === 'personal_instructions_missing') {
            row = {
              line: item.line,
              status: 'skipped',
              title: res.account.email || res.account.name || t('modal.add.duplicate_title', { line: item.line }),
              detail: t('modal.add.missing_skipped'),
            }
          } else {
            added++
            let personalInstructionsDetail = ''
            if (res.personal_instructions_checked) {
              if (res.personal_instructions_check_error) {
                personalInstructionsDetail = t('modal.add.check_failed_kept')
              } else {
                personalInstructionsDetail = res.personal_instructions_configured
                  ? t('modal.add.configured_suffix')
                  : t('modal.add.missing_suffix')
              }
            }
            row = {
              line: item.line,
              status: 'success',
              title: res.account.email || res.account.name || t('modal.add.line', { line: item.line }),
              detail: res.imported && res.imported > 1
                ? `${t('modal.add.multiple_workspaces', { count: res.imported })} · ${res.accounts?.map(account => `${account.space || t('modal.add.unnamed_space')} (${account.plan_type || t('modal.add.unknown_plan')})`).join('、')}${personalInstructionsDetail}`
                : `${res.account.space || t('modal.add.unnamed_space')} · ${res.account.plan_type || t('modal.add.unknown_plan')}${personalInstructionsDetail}`,
            }
          }
        } catch (err) {
          row = {
            line: item.line,
            status: 'error',
            title: t('modal.add.failed_title', { line: item.line }),
            detail: err instanceof Error ? err.message : t('modal.add.request_failed'),
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
            <h2 className="text-[16px] font-semibold">{t('modal.add.title')}</h2>
            <div className="text-[11px] text-text-muted mt-0.5">{t('modal.add.subtitle')}</div>
          </div>
          <button disabled={loading} onClick={onClose} className="text-text-muted hover:text-white bg-transparent border-none cursor-pointer text-lg px-1 disabled:opacity-30">×</button>
        </div>

        <div className="text-[12px] text-text-secondary mb-4 space-y-1.5">
          <p>{t('modal.add.intro')}</p>
          <p className="text-text-muted">{t('modal.add.how_to')}</p>
        </div>

        <div className="mb-4 rounded-lg border border-white/10 bg-white/[.025] p-3">
            <div className="text-[12px] font-medium text-text-primary">{t('modal.add.check_enabled')}</div>
            <div className="text-[11px] text-text-muted mt-1 mb-2.5">{t('modal.add.check_help')}</div>
            <div className="grid grid-cols-2 gap-2 max-sm:grid-cols-1">
              <button
                type="button"
                disabled={loading}
                onClick={() => setPersonalInstructionsPolicy('all')}
                className={`rounded-md border px-3 py-2 text-left cursor-pointer disabled:opacity-50 ${personalInstructionsPolicy === 'all' ? 'border-notion-blue/60 bg-notion-blue/10' : 'border-border bg-bg-card hover:bg-bg-card-hover'}`}
              >
                <div className="text-[12px] font-medium text-text-primary">{t('modal.add.import_all')}</div>
                <div className="text-[10px] text-text-muted mt-0.5">{t('modal.add.import_all_help')}</div>
              </button>
              <button
                type="button"
                disabled={loading}
                onClick={() => setPersonalInstructionsPolicy('configured_only')}
                className={`rounded-md border px-3 py-2 text-left cursor-pointer disabled:opacity-50 ${personalInstructionsPolicy === 'configured_only' ? 'border-notion-blue/60 bg-notion-blue/10' : 'border-border bg-bg-card hover:bg-bg-card-hover'}`}
              >
                <div className="text-[12px] font-medium text-text-primary">{t('modal.add.configured_only')}</div>
                <div className="text-[10px] text-text-muted mt-0.5">{t('modal.add.configured_only_help')}</div>
              </button>
            </div>
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
            placeholder={t('modal.add.placeholder')}
            rows={7}
            disabled={loading}
            className="w-full py-2.5 px-3 bg-transparent border border-white/10 rounded-lg text-[13px] text-text-primary outline-none focus:border-white/30 focus:ring-1 focus:ring-white/10 transition-all placeholder:text-white/25 resize-y font-mono disabled:opacity-60"
          />
          <div className="flex items-center justify-between gap-3 mt-1.5 text-[11px] text-text-muted">
            <span>{t('modal.add.valid_rows', { rows: parsedLines.length, count: uniqueTokenCount })}</span>
            {loading && <span className="text-notion-blue">{t('modal.add.processing', { done: progress.done, total: progress.total })}</span>}
          </div>
          {error && (
            <div className="text-err text-[12px] mt-2 px-1">{error}</div>
          )}

          {results.length > 0 && (
            <div className="mt-3 border border-white/10 rounded-lg overflow-hidden">
              <div className="flex items-center justify-between gap-3 px-3 py-2 bg-white/[.035] text-[11px]">
                <span className="text-text-secondary">{t('modal.add.results')}</span>
                <span className="tabular-nums">
                  <span className="text-ok">{t('modal.add.succeeded', { count: successCount })}</span>
                  <span className="text-text-muted"> · </span>
                  <span className={failureCount > 0 ? 'text-err' : 'text-text-muted'}>{t('modal.add.failed', { count: failureCount })}</span>
                  {skippedCount > 0 && <span className="text-text-muted"> · {t('modal.add.skipped', { count: skippedCount })}</span>}
                </span>
              </div>
              <div className="max-h-48 overflow-auto divide-y divide-white/[.05]">
                {results.map(item => (
                  <div key={`${item.line}-${item.status}`} className="px-3 py-2 text-[11px]">
                    <div className="flex items-center gap-2">
                      <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${item.status === 'success' ? 'bg-ok' : item.status === 'error' ? 'bg-err' : 'bg-text-muted'}`} />
                      <span className={item.status === 'error' ? 'text-err' : 'text-text-primary'}>{item.title}</span>
                      <span className="ml-auto text-text-muted tabular-nums">{t('modal.add.line', { line: item.line })}</span>
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
              {completed ? t('common.close') : t('common.cancel')}
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
                {t('modal.add.continue')}
              </button>
            ) : (
              <button
                type="submit"
                disabled={loading || uniqueTokenCount === 0}
                className="flex-1 py-2.5 bg-white hover:bg-white/90 text-black rounded-lg text-[13px] font-semibold cursor-pointer transition-colors border-none disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {loading
                  ? t('modal.add.importing', { done: progress.done, total: progress.total })
                  : t('modal.add.import_count', { count: uniqueTokenCount || '' })}
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  )
}

type CleanupTarget = 'auth_invalid' | 'exhausted' | 'no_workspace' | 'personal_missing'

function CleanupModal({
  counts,
  busy,
  onClose,
  onRun,
}: {
  counts: Record<CleanupTarget, number>
  busy: boolean
  onClose: () => void
  onRun: (target: CleanupTarget) => Promise<void>
}) {
  const { t } = useTranslation()
  const [target, setTarget] = useState<CleanupTarget>(() =>
    (['auth_invalid', 'exhausted', 'no_workspace', 'personal_missing'] as CleanupTarget[])
      .find(option => counts[option] > 0) ?? 'auth_invalid',
  )
  const options: Array<{ target: CleanupTarget; title: string; help: string; tone: string }> = [
    { target: 'auth_invalid', title: t('cleanup.auth_invalid'), help: t('cleanup.auth_invalid_help'), tone: 'bg-err' },
    { target: 'exhausted', title: t('cleanup.exhausted'), help: t('cleanup.exhausted_help'), tone: 'bg-warn' },
    { target: 'no_workspace', title: t('cleanup.no_workspace'), help: t('cleanup.no_workspace_help'), tone: 'bg-err' },
    { target: 'personal_missing', title: t('cleanup.personal_missing'), help: t('cleanup.personal_missing_help'), tone: 'bg-text-muted' },
  ]
  const selected = options.find(option => option.target === target)!

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) onClose()
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [busy, onClose])

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm px-4" onClick={() => { if (!busy) onClose() }}>
      <div className="w-full max-w-lg rounded-xl border border-white/10 bg-[#1a1a1a] p-5 shadow-2xl max-sm:p-4" onClick={event => event.stopPropagation()}>
        <div className="flex items-start justify-between gap-4 mb-4">
          <div>
            <h2 className="text-[16px] font-semibold text-text-primary">{t('cleanup.title')}</h2>
            <p className="text-[11px] text-text-muted mt-1">{t('cleanup.subtitle')}</p>
          </div>
          <button type="button" disabled={busy} onClick={onClose} className="text-text-muted hover:text-white bg-transparent border-none cursor-pointer text-lg px-1 disabled:opacity-30">×</button>
        </div>
        <div className="divide-y divide-white/[.05] rounded-lg border border-white/[.07] overflow-hidden">
          {options.map(option => (
            <button
              key={option.target}
              type="button"
              onClick={() => setTarget(option.target)}
              className={`w-full flex items-center gap-3 px-3.5 py-3 text-left border-none cursor-pointer transition-colors ${target === option.target ? 'bg-white/[.07]' : 'bg-bg-card hover:bg-bg-card-hover'}`}
            >
              <span className={`w-2 h-2 rounded-full shrink-0 ${option.tone}`} />
              <span className="min-w-0 flex-1">
                <span className="block text-[12px] font-medium text-text-primary">{option.title}</span>
                <span className="block text-[10px] text-text-muted mt-0.5">{option.help}</span>
              </span>
              <span className={`text-[12px] font-semibold tabular-nums ${counts[option.target] > 0 ? 'text-text-primary' : 'text-text-muted'}`}>{counts[option.target]}</span>
              <span className={`w-3.5 h-3.5 rounded-full border flex items-center justify-center ${target === option.target ? 'border-notion-blue' : 'border-white/20'}`}>
                {target === option.target && <span className="w-1.5 h-1.5 rounded-full bg-notion-blue" />}
              </span>
            </button>
          ))}
        </div>
        <div className="flex gap-2.5 mt-4">
          <button type="button" onClick={onClose} disabled={busy} className="flex-1 py-2.5 bg-transparent hover:bg-white/5 text-text-secondary rounded-lg text-[12px] font-medium cursor-pointer border border-white/10 disabled:opacity-40">
            {t('common.cancel')}
          </button>
          <button
            type="button"
            disabled={busy || counts[target] === 0}
            onClick={() => void onRun(target)}
            className="flex-1 py-2.5 bg-err/15 hover:bg-err/25 text-err rounded-lg text-[12px] font-semibold cursor-pointer border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {busy ? t('cleanup.starting') : t('cleanup.run', { type: selected.title, count: counts[target] })}
          </button>
        </div>
      </div>
    </div>
  )
}

// --- Login Page ---

function LoginPage({ onSuccess }: { onSuccess: () => void }) {
  const { t } = useTranslation()
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
      setError(result.error || t('auth.wrong_password'))
      setPassword('')
      inputRef.current?.focus()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('auth.login_failed'))
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
          <p className="text-[13px] text-text-muted mt-1">{t('auth.prompt')}</p>
        </div>
        <form onSubmit={handleSubmit}>
          <div className="relative mb-4">
            <input
              ref={inputRef}
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder={t('auth.placeholder')}
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
            {loading ? t('auth.logging_in') : t('auth.login')}
          </button>
        </form>
      </div>
    </div>
  )
}

// --- Header ---

type DashboardPage = 'dashboard' | 'accounts' | 'settings'

function dashboardPageFromHash(): DashboardPage {
  if (window.location.hash === '#accounts') return 'accounts'
  if (window.location.hash === '#settings') return 'settings'
  return 'dashboard'
}

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
  const { t } = useTranslation()
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
    <header className="sticky top-0 z-50 flex items-center gap-5 px-6 py-2.5 border-b border-border bg-bg-secondary/95 backdrop-blur-xl max-md:flex-wrap max-md:gap-2 max-md:px-3 max-sm:py-2">
      <div className="flex items-center gap-2.5 min-w-0 max-md:flex-1 max-sm:hidden">
        <div className="w-7 h-7 bg-[#333] rounded-md flex items-center justify-center text-sm font-extrabold text-white">N</div>
        <span className="text-[15px] font-semibold tracking-tight">
          notion-manager
          <span className="text-text-secondary font-normal text-[13px] ml-1.5 max-sm:hidden">dashboard</span>
        </span>
        <span
          className="text-[10px] text-text-muted font-mono bg-white/[.04] border border-white/[.06] rounded px-1.5 py-0.5"
          title={t('header.version', { version })}
        >
          {displayVersion(version)}
        </span>
      </div>
      <nav className="flex items-center rounded-lg bg-black/20 p-1 border border-white/[.05] max-md:order-2 max-md:w-full max-sm:order-1">
        <button
          onClick={() => onPageChange('dashboard')}
          className={`inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-md text-[12px] font-medium cursor-pointer border-none transition-colors max-md:flex-1 ${activePage === 'dashboard' ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
        >
          <IconDashboard /> {t('header.dashboard')}
        </button>
        <button
          onClick={() => onPageChange('accounts')}
          className={`px-3 py-1.5 rounded-md text-[12px] font-medium cursor-pointer border-none transition-colors max-md:flex-1 ${activePage === 'accounts' ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
        >
          {t('header.accounts')}
        </button>
        <button
          onClick={() => onPageChange('settings')}
          className={`px-3 py-1.5 rounded-md text-[12px] font-medium cursor-pointer border-none transition-colors max-md:flex-1 ${activePage === 'settings' ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
        >
          {t('header.settings')}
        </button>
      </nav>
      <div className="flex items-center gap-3 ml-auto max-md:order-3 max-md:ml-0 max-md:w-full max-sm:order-2 max-sm:gap-2 max-sm:justify-end">
        {activePage === 'accounts' && <div className="relative w-72 max-md:flex-1">
          <svg className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" />
          </svg>
          <input
            ref={inputRef}
            value={query}
            onChange={e => onQuery(e.target.value)}
            placeholder={t('header.search_placeholder')}
            className="w-full py-1.5 pl-8 pr-10 bg-bg-input border border-border rounded-md text-[13px] text-text-primary outline-none focus:border-white/20 transition-colors placeholder:text-text-muted"
          />
          <kbd className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[11px] text-text-muted bg-bg-card border border-border rounded px-1.5 py-0.5 max-sm:hidden">/</kbd>
        </div>}
        {authRequired && (
          <button
            onClick={onLogout}
            className="text-[12px] text-text-secondary hover:text-text-primary cursor-pointer transition-colors bg-transparent border-none px-2 py-1"
            title={t('header.logout_title')}
          >
            {t('header.logout')}
          </button>
        )}
        <LanguageToggle />
      </div>
    </header>
  )
}

function StatCard({ label, value, sub, color, icon }: { label: string; value: string | number; sub: string; color?: string; icon?: React.ReactNode }) {
  return (
    <div className="px-6 py-5 max-sm:px-3.5 max-sm:py-3">
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
  const { t } = useTranslation()
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
          {fmt(remaining)} <span className="text-text-muted font-normal">/ {fmt(limit)} {t('stats.remaining')}</span>
        </span>
      </div>
      <div className="h-[2px] bg-white/[.06] rounded-full overflow-hidden">
        <div className={`h-full rounded-full transition-all duration-500 ${fillClass}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function TotalQuotaBar({ summary }: { summary?: AccountSummary | null }) {
  const { t } = useTranslation()
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
    <div className="mb-5 space-y-3 max-sm:mb-3 max-sm:space-y-2">
      <div className="flex justify-between items-center">
        <span className="text-[11px] text-text-secondary uppercase tracking-wider flex items-center gap-1.5"><IconBarChart /> {t('stats.diagnostics')}</span>
        {totalPremiumLimit > 0 && (
          <span className="text-[12px] text-text-muted tabular-nums max-sm:hidden">
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
      <div className="text-[10px] text-text-muted leading-relaxed max-sm:hidden">
        {t('stats.diagnostics_note')}
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

interface AccountGroup {
  id: string
  accounts: AccountInfo[]
}

function workspacePlanPriority(plan: string): number {
  switch ((plan || '').trim().toLowerCase()) {
    case 'enterprise': return 50
    case 'business': return 40
    case 'team': return 30
    case 'plus':
    case 'personal_pro': return 20
    case 'personal':
    case 'free': return 10
    default: return 0
  }
}

function isWorkspaceUsable(account: AccountInfo): boolean {
  const quotaBlocked = !account.quota_unlimited && (account.permanent || account.exhausted)
  return !account.disabled && !account.auth_invalid && !account.no_workspace &&
    !account.ai_disabled && !account.temporarily_unavailable && !quotaBlocked
}

function accountLoginKey(account: AccountInfo): string {
  const loginID = account.login_id?.trim()
  if (loginID) return `login:${loginID}`
  const token = account.token_v2?.trim()
  if (token) return `token:${token}`
  const email = account.email?.trim().toLowerCase()
  if (email) return `email:${email}`
  return `workspace:${account.account_id}`
}

function groupWorkspaceAccounts(accounts: AccountInfo[]): AccountGroup[] {
  const grouped = new Map<string, AccountGroup>()
  const seenAccountIDs = new Set<string>()
  for (const account of accounts) {
    if (!account.account_id || seenAccountIDs.has(account.account_id)) continue
    seenAccountIDs.add(account.account_id)
    const key = accountLoginKey(account)
    const existing = grouped.get(key)
    if (existing) {
      existing.accounts.push(account)
    } else {
      grouped.set(key, { id: key, accounts: [account] })
    }
  }
  return Array.from(grouped.values())
}

function AccountCard({
  account,
  onChanged,
  selected,
  onToggleSelected,
  embedded = false,
  headerAction,
}: {
  account: AccountInfo
  onChanged: () => void
  selected: boolean
  onToggleSelected: () => void
  embedded?: boolean
  headerAction?: React.ReactNode
}) {
  const { t, i18n } = useTranslation()
  const [showModels, setShowModels] = useState(false)
  const spaceQuota = getSpaceQuota(account)
  const userQuota = getUserQuota(account)
  const sameBasicQuota = isSameQuota(spaceQuota, userQuota)
  const premium = hasPremiumAccess(account)
  const fullNotionAI = !!account.quota_unlimited
  const noWorkspace = !!account.no_workspace
  const aiDisabled = !!account.ai_disabled
  const authInvalid = !!account.auth_invalid
  const manuallyDisabled = !!account.disabled
  const temporarilyUnavailable = !authInvalid && !!account.temporarily_unavailable
  const quotaBlocked = !fullNotionAI && (account.permanent || account.exhausted)
  const status = manuallyDisabled || quotaBlocked || noWorkspace || aiDisabled || authInvalid || temporarilyUnavailable
    ? 'exhausted'
    : fullNotionAI ? 'ok' : mergeQuotaStatus([
      getQuotaStatusByUsage(spaceQuota.usage, spaceQuota.limit),
      getQuotaStatusByUsage(userQuota.usage, userQuota.limit),
    ])
  const modelCount = account.models?.length || 0

  const dotCls = status === 'exhausted' ? 'bg-err' : status === 'low' ? 'bg-err' : 'bg-ok'
  // no_workspace shares the exhausted card style so the operator
  // immediately sees the account is unhealthy. Click-through is blocked
  // because Notion's /ai SPA hangs indefinitely on these accounts (the
  // root-cause this fix is for).
  const cardBg = quotaBlocked && account.permanent ? 'bg-bg-exhausted border-white/[0.03] opacity-55'
    : manuallyDisabled || quotaBlocked || noWorkspace || aiDisabled || authInvalid || temporarilyUnavailable ? 'bg-bg-exhausted border-white/[0.03]'
    : 'bg-bg-card hover:bg-bg-card-hover border-white/[0.03] hover:border-white/[0.07]'

  const handleClick = () => {
    if (manuallyDisabled) {
      alert(t('account.manual_disabled_alert'))
      return
    }
    if (authInvalid) {
      alert(t('account.auth_invalid_alert'))
      return
    }
    if (noWorkspace) {
      // Use a native alert — we don't have a toast infra and openProxy
      // would otherwise pop a tab that displays raw JSON 409 to the user.
      alert(t('account.no_workspace_alert'))
      return
    }
    if (aiDisabled) {
      alert(t('account.ai_disabled_alert'))
      return
    }
    if (temporarilyUnavailable) {
      alert(t('account.temporary_alert', { reason: account.last_failure_reason || 'temporary_failure' }))
      return
    }
    openProxy(account.account_id)
  }

  return (
    <div
      className={`${embedded ? 'rounded-md p-3' : 'rounded-lg p-4 max-sm:p-3'} border ${manuallyDisabled || authInvalid || noWorkspace || aiDisabled || temporarilyUnavailable ? 'cursor-not-allowed' : `cursor-pointer ${embedded ? 'hover:border-white/[0.12]' : 'hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/30'}`} transition-all duration-200 ${selected ? 'ring-1 ring-notion-blue border-notion-blue/60' : ''} ${cardBg}`}
      onClick={handleClick}
      title={manuallyDisabled ? t('account.manual_disabled_tooltip') : authInvalid ? t('account.auth_invalid_tooltip') : noWorkspace ? t('account.no_workspace_tooltip') : aiDisabled ? t('account.ai_disabled_tooltip') : temporarilyUnavailable ? t('account.temporary_tooltip', { reason: account.last_failure_reason || 'temporary_failure' }) : undefined}
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
          aria-label={t('account.select', { email: account.email || t('account.email_unavailable') })}
          title={t('account.select_action')}
        />
        {!embedded && (
          <div
            className="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold text-white shrink-0"
            style={{ background: avatarColor(account.name) }}
          >
            {avatarLetter(account.name)}
          </div>
        )}
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-semibold truncate">
            {embedded ? (account.space || t('account.unnamed_workspace')) : (account.name || t('account.unnamed_login'))}
            {!embedded && account.space && <span className="text-text-secondary font-normal"> · {account.space}</span>}
          </div>
          <div className="text-[11px] text-text-secondary truncate">
            {embedded
              ? t('account.workspace_identity', { id: account.space_id_short || account.account_id.slice(0, 8) })
              : (account.email || t('account.email_unavailable'))}
          </div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {headerAction}
          <div className={`w-2 h-2 rounded-full ${dotCls}`} />
          <AccountMenu account={account} onChanged={onChanged} />
        </div>
      </div>

      {/* Badges */}
      <div className="flex gap-3 flex-wrap mt-3 mb-2.5 items-center">
        <Badge variant="plan">{account.plan || 'unknown'}</Badge>
        <Badge variant={fullNotionAI ? 'premium' : 'plan'}>
          {fullNotionAI ? t('account.full_ai') : t('account.limited_trial')}
        </Badge>
        {account.registered_via && (
          <Badge variant="plan">via {providerDisplay(account.registered_via)}</Badge>
        )}
        <span title={
          account.personal_instructions_check_error
            ? account.personal_instructions_check_error
            : account.personal_instructions_checked_at
              ? t('account.instructions_checked', { date: new Date(account.personal_instructions_checked_at).toLocaleString(i18n.language === 'zh' ? 'zh-CN' : 'en-US', { hour12: false }) })
              : t('account.instructions_unchecked')
        }>
          {account.personal_instructions_check_error ? (
            <Badge variant="warning">{t('account.instructions_failed')}</Badge>
          ) : account.personal_instructions_configured === true ? (
            <Badge variant="ok">{t('account.instructions_configured')}</Badge>
          ) : account.personal_instructions_configured === false ? (
            <Badge variant="plan">{t('account.instructions_missing')}</Badge>
          ) : (
            <Badge variant="plan">{t('account.instructions_not_checked')}</Badge>
          )}
        </span>
        {premium && <Badge variant="premium">{t('account.premium_signal')}</Badge>}
        {(account.research_usage != null && account.research_usage > 0) && (
          <Badge variant="research">
            <IconFlask /> {t('account.research_usage', { count: account.research_usage })}
          </Badge>
        )}
        {account.exhausted && !account.permanent && <Badge variant="warning">{t('account.ai_unavailable')}</Badge>}
        {account.permanent && <Badge variant="warning">{t('account.trial_exhausted')}</Badge>}
        {manuallyDisabled && <Badge variant="warning">{t('account.manually_disabled')}</Badge>}
        {authInvalid && <Badge variant="warning">{t('account.cookie_invalid')}</Badge>}
        {noWorkspace && <Badge variant="warning">{t('account.no_workspace')}</Badge>}
        {aiDisabled && <Badge variant="warning">{t('account.ai_disabled')}</Badge>}
        {temporarilyUnavailable && (
          <Badge variant="warning">{t('account.temporarily_skipped', { reason: account.last_failure_reason || 'failure' })}</Badge>
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
      {fullNotionAI ? (
        <div className="mb-1.5 flex items-center justify-between rounded-md bg-ok/5 px-2.5 py-2 text-[11px]">
          <span className="text-text-muted">{t('account.ai_allowance')}</span>
          <span className="font-semibold text-ok">{t('account.unlimited_now')}</span>
        </div>
      ) : sameBasicQuota ? (
        <QuotaBar label="Basic raw" usage={spaceQuota.usage} limit={spaceQuota.limit} />
      ) : (
        <>
          <QuotaBar label="Space raw" usage={spaceQuota.usage} limit={spaceQuota.limit} />
          {userQuota.limit > 0 && <QuotaBar label="User raw" usage={userQuota.usage} limit={userQuota.limit} />}
        </>
      )}
      {premium && <QuotaBar label="Premium monthlyAllocated raw" labelClass="text-[#7eb8ff]" usage={account.premium_usage} limit={account.premium_limit} />}
      <div className="flex flex-wrap gap-3 mt-2 text-[10px] text-text-muted">
        {!fullNotionAI && <span>{t('account.basic_estimated', { count: fmt(account.remaining || 0) })}</span>}
        {premium && <span>{t('account.premium_raw', { count: fmt(account.premium_balance || 0) })}</span>}
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
          <span className="truncate">{t('account.last_checked', { date: formatCheckedAt(account.checked_at) })} · {t('account.recent_ai', { date: formatTimestampMs(account.last_usage_at) })}</span>
        </span>
        {manuallyDisabled ? (
          <span className="text-[11px] text-err font-medium">{t('account.disabled')}</span>
        ) : noWorkspace ? (
          <span className="text-[11px] text-err font-medium">{t('account.unavailable')}</span>
        ) : (
          <span className="text-[11px] text-text-secondary hover:text-white font-medium transition-colors">{t('account.open_proxy')}</span>
        )}
      </div>
    </div>
  )
}

function AccountGroupCard({
  group,
  selectedAccounts,
  onToggleWorkspace,
  onChanged,
}: {
  group: AccountGroup
  selectedAccounts: Map<string, string>
  onToggleWorkspace: (account: AccountInfo) => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const orderedAccounts = useMemo(
    () => [...group.accounts].sort((left, right) => {
      const usability = Number(isWorkspaceUsable(right)) - Number(isWorkspaceUsable(left))
      if (usability !== 0) return usability
      const tier = workspacePlanPriority(right.plan) - workspacePlanPriority(left.plan)
      if (tier !== 0) return tier
      return (left.space || '').localeCompare(right.space || '')
    }),
    [group.accounts],
  )
  const [activeAccountID, setActiveAccountID] = useState(() => orderedAccounts[0]?.account_id || '')
  const [switcherOpen, setSwitcherOpen] = useState(false)
  const switcherRef = useRef<HTMLDivElement>(null)
  const activeAccount = orderedAccounts.find(account => account.account_id === activeAccountID) || orderedAccounts[0]

  useEffect(() => {
    if (!activeAccount || activeAccount.account_id === activeAccountID) return
    setActiveAccountID(activeAccount.account_id)
  }, [activeAccount, activeAccountID])

  useEffect(() => {
    if (!switcherOpen) return
    const closeOnOutside = (event: PointerEvent) => {
      if (!switcherRef.current?.contains(event.target as Node)) setSwitcherOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setSwitcherOpen(false)
    }
    window.addEventListener('pointerdown', closeOnOutside)
    window.addEventListener('keydown', closeOnEscape)
    return () => {
      window.removeEventListener('pointerdown', closeOnOutside)
      window.removeEventListener('keydown', closeOnEscape)
    }
  }, [switcherOpen])

  if (!activeAccount) return null

  const workspaceSwitcher = orderedAccounts.length > 1 ? (
    <div ref={switcherRef} className="relative">
      <button
        type="button"
        onClick={event => {
          event.stopPropagation()
          setSwitcherOpen(open => !open)
        }}
        className={`inline-flex items-center gap-1 px-2 py-1 rounded border text-[10px] cursor-pointer transition-colors ${switcherOpen ? 'bg-white/10 border-white/15 text-text-primary' : 'bg-white/[.04] border-white/[.07] text-text-secondary hover:text-text-primary hover:bg-white/[.07]'}`}
        aria-haspopup="listbox"
        aria-expanded={switcherOpen}
        title={t('account.switch_workspace')}
      >
        {t('account.workspaces_count', { count: orderedAccounts.length })} <span>{switcherOpen ? '▴' : '▾'}</span>
      </button>
      {switcherOpen && (
        <div
          className="absolute right-0 top-[calc(100%+6px)] z-40 w-72 max-w-[75vw] max-h-72 overflow-auto rounded-lg border border-white/10 bg-[#202020] shadow-2xl shadow-black/50 p-1.5"
          role="listbox"
          onClick={event => event.stopPropagation()}
        >
          {orderedAccounts.map(account => {
            const usable = isWorkspaceUsable(account)
            const current = account.account_id === activeAccount.account_id
            return (
              <button
                key={account.account_id}
                type="button"
                role="option"
                aria-selected={current}
                onClick={() => {
                  setActiveAccountID(account.account_id)
                  setSwitcherOpen(false)
                }}
                className={`w-full flex items-center gap-2.5 rounded-md px-2.5 py-2 text-left border-none cursor-pointer transition-colors ${current ? 'bg-notion-blue/15' : 'bg-transparent hover:bg-white/[.05]'}`}
              >
                <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${usable ? 'bg-ok' : 'bg-err'}`} />
                <span className="min-w-0 flex-1">
                  <span className="block text-[11px] font-medium text-text-primary truncate">{account.space || t('account.unnamed_workspace')}</span>
                  <span className="block text-[10px] text-text-muted truncate">
                    {account.plan || 'unknown'} · {account.quota_unlimited
                      ? t('account.unlimited_now')
                      : t('account.basic_estimated', { count: fmt(account.remaining || 0) })}
                  </span>
                </span>
                {current && <span className="text-[10px] text-notion-blue">✓</span>}
              </button>
            )
          })}
        </div>
      )}
    </div>
  ) : undefined

  return (
    <div className="relative w-full">
      <AccountCard
        key={activeAccount.account_id}
        account={activeAccount}
        onChanged={onChanged}
        selected={selectedAccounts.has(activeAccount.account_id)}
        onToggleSelected={() => onToggleWorkspace(activeAccount)}
        headerAction={workspaceSwitcher}
      />
    </div>
  )
}

const accountBatchActionKeys: Record<AccountBatchJobAction, string> = {
  check_personal_instructions: 'batch.check',
  disable: 'batch.disable',
  enable: 'batch.enable',
  delete: 'batch.delete',
  delete_missing_personal_instructions: 'batch.delete_missing',
  delete_exhausted: 'batch.delete_exhausted',
  delete_no_workspace: 'batch.delete_no_workspace',
}

const accountStatusFilterOptions: Array<{ value: AccountStatusFilter; labelKey: string }> = [
  { value: 'all', labelKey: 'common.all_accounts' },
  { value: 'available', labelKey: 'common.available_accounts' },
  { value: 'disabled', labelKey: 'common.manual_disabled' },
  { value: 'exhausted', labelKey: 'common.quota_exhausted' },
  { value: 'auth_invalid', labelKey: 'common.cookie_invalid' },
  { value: 'no_workspace', labelKey: 'common.no_workspace' },
  { value: 'ai_disabled', labelKey: 'common.ai_disabled' },
  { value: 'temporarily_unavailable', labelKey: 'common.temporary' },
  { value: 'personal_configured', labelKey: 'common.instructions_configured' },
  { value: 'personal_missing', labelKey: 'common.instructions_missing' },
  { value: 'personal_failed', labelKey: 'common.instructions_failed' },
  { value: 'personal_unchecked', labelKey: 'common.instructions_unchecked' },
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
  const { t } = useTranslation()
  const pct = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : 0
  const activeSteps = job.steps.filter(step => step.status === 'running').slice(0, 3)
  const failedSteps = job.steps.filter(step => step.status === 'failed').slice(0, 3)
  const isPersonal = job.action === 'check_personal_instructions' || job.action === 'delete_missing_personal_instructions'
  return (
    <div className="mb-4 rounded-lg border border-notion-blue/35 bg-notion-blue/[.07] p-3.5">
      <div className="flex items-start gap-3 max-sm:flex-col">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[13px] font-semibold text-text-primary">{t(accountBatchActionKeys[job.action])}</span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded ${job.state === 'running' ? 'text-notion-blue bg-notion-blue/10' : job.failed > 0 ? 'text-warn bg-warn/10' : 'text-ok bg-ok/10'}`}>
              {job.state === 'running' ? t('batch.running') : job.state === 'interrupted' ? t('batch.interrupted') : t('batch.completed')}
            </span>
            <span className="text-[10px] text-text-muted">{t('batch.concurrency', { count: job.concurrency })}</span>
          </div>
          <div className="mt-2 flex items-center gap-3 text-[11px] text-text-secondary flex-wrap">
            <span>{t('batch.progress')} <strong className="text-text-primary tabular-nums">{job.done} / {job.total}</strong></span>
            <span className="text-ok">{t('batch.succeeded', { count: job.succeeded })}</span>
            {job.skipped > 0 && <span className="text-text-muted">{t('batch.kept', { count: job.skipped })}</span>}
            {job.failed > 0 && <span className="text-err">{t('batch.failed', { count: job.failed })}</span>}
            {isPersonal && <><span>{t('batch.configured', { count: job.configured })}</span><span>{t('batch.missing', { count: job.missing })}</span></>}
          </div>
          <div className="mt-2 h-1.5 bg-white/[.07] rounded-full overflow-hidden">
            <div className="h-full bg-notion-blue rounded-full transition-all duration-300" style={{ width: `${pct}%` }} />
          </div>
          {activeSteps.length > 0 && (
            <div className="mt-2 text-[10px] text-text-muted truncate">
              {t('batch.processing', { accounts: activeSteps.map(step => step.email).join(', ') })}
            </div>
          )}
          {job.state !== 'running' && failedSteps.length > 0 && (
            <div className="mt-2 text-[10px] text-err space-y-0.5">
              {failedSteps.map((step, index) => (
                <div key={step.account_id || `${step.email}-${index}`} className="truncate" title={step.message}>
                  {step.email || t('account.email_unavailable')}: {step.message || t('batch.step_failed')}
                </div>
              ))}
              {job.failed > failedSteps.length && <div>{t('batch.other_failed', { count: job.failed - failedSteps.length })}</div>}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0 max-sm:w-full max-sm:grid max-sm:grid-cols-2">
          {job.state !== 'running' && job.failed > 0 && (
            <button onClick={onRetry} disabled={retrying} className="px-3 py-1.5 bg-warn/10 hover:bg-warn/20 text-warn rounded-md text-[12px] cursor-pointer border border-warn/25 disabled:opacity-40">
              {retrying ? t('batch.retrying') : t('batch.retry_failed', { count: job.failed })}
            </button>
          )}
          {job.state !== 'running' && (
            <button onClick={onClose} className="px-3 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer border border-border">
              {t('common.close')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

interface PoolSummaryView {
  exhausted: number
  exhaustedOnly: number
  noWorkspace: number
  aiDisabled: number
  authInvalid: number
  disabled: number
  otherUnavailable: number
  availableRate: number
  totalResearchUsage: number
  totalRemaining: number
  totalSpaceRemaining: number
  totalUserRemaining: number
  totalPremiumBalance: number
  totalPremiumLimit: number
  premiumAccounts: number
  unlimitedAccounts: number
  sameBasicQuota: boolean
}

function DashboardHome({
  data,
  summary,
  loginCount,
  tokenStats,
  settings,
  versionStatus,
  appVersion,
  refreshStatus,
  refreshing,
  quotaRefreshing,
  refreshTime,
  onOpenBest,
  onRefreshQuota,
  onRefreshData,
  onAddAccount,
  onManageAccounts,
  onShowAccounts,
  onOpenHistory,
  onOpenSettings,
}: {
  data: DashboardData
  summary: PoolSummaryView
  loginCount: number
  tokenStats: TokenStats | null
  settings: SearchSettings | null
  versionStatus: DeploymentVersionStatus | null
  appVersion: string
  refreshStatus: RefreshStatus | null
  refreshing: boolean
  quotaRefreshing: boolean
  refreshTime: string
  onOpenBest: () => void
  onRefreshQuota: () => void
  onRefreshData: () => void
  onAddAccount: () => void
  onManageAccounts: () => void
  onShowAccounts: (status: AccountStatusFilter) => void
  onOpenHistory: () => void
  onOpenSettings: () => void
}) {
  const { t, i18n } = useTranslation()
  const allIssues: Array<{
    status: AccountStatusFilter
    count: number
    label: string
    detail: string
    dot: string
  }> = [
    { status: 'exhausted', count: summary.exhaustedOnly, label: t('dashboard.issue_exhausted'), detail: t('dashboard.issue_exhausted_help'), dot: 'bg-warn' },
    { status: 'auth_invalid', count: summary.authInvalid, label: t('dashboard.issue_auth'), detail: t('dashboard.issue_auth_help'), dot: 'bg-err' },
    { status: 'no_workspace', count: summary.noWorkspace, label: t('dashboard.issue_workspace'), detail: t('dashboard.issue_workspace_help'), dot: 'bg-err' },
    { status: 'ai_disabled', count: summary.aiDisabled, label: t('dashboard.issue_ai'), detail: t('dashboard.issue_ai_help'), dot: 'bg-warn' },
    { status: 'disabled', count: summary.disabled, label: t('dashboard.issue_disabled'), detail: t('dashboard.issue_disabled_help'), dot: 'bg-text-muted' },
    { status: 'temporarily_unavailable', count: summary.otherUnavailable, label: t('dashboard.issue_temporary'), detail: t('dashboard.issue_temporary_help'), dot: 'bg-notion-blue' },
  ]
  const issues = allIssues.filter(issue => issue.count > 0)
  const attentionTotal = issues.reduce((total, issue) => total + issue.count, 0)
  const healthSegments = [
    { key: 'available', count: data.available, color: 'bg-ok', label: t('dashboard.health_available') },
    { key: 'exhausted', count: summary.exhaustedOnly, color: 'bg-warn', label: t('dashboard.health_exhausted') },
    { key: 'auth', count: summary.authInvalid + summary.noWorkspace, color: 'bg-err', label: t('dashboard.health_blocked') },
    { key: 'other', count: summary.aiDisabled + summary.disabled + summary.otherUnavailable, color: 'bg-text-muted', label: t('dashboard.health_other') },
  ].filter(segment => segment.count > 0)
  const lastSevenDays = tokenStats?.by_day.slice(-7) ?? []
  const maxDayTokens = Math.max(1, ...lastSevenDays.map(day => day.total))
  const requestCount = tokenStats?.last_24h.requests
  const toolPolicy = settings?.tool_choice_policy
    ? t(`api.tool_choice_${settings.tool_choice_policy}`)
    : t('dashboard.unknown')
  const version = displayVersion(versionStatus?.current_version || appVersion)

  return (
    <div>
      <div className="flex items-start justify-between gap-5 mb-6 max-md:flex-col max-md:items-stretch">
        <div>
          <h1 className="text-[20px] font-semibold text-text-primary">{t('dashboard.title')}</h1>
          <p className="text-[12px] text-text-muted mt-1">{t('dashboard.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2 flex-wrap max-sm:grid max-sm:grid-cols-2">
          <button
            onClick={onOpenBest}
            disabled={data.available === 0}
            className="inline-flex items-center justify-center gap-1.5 px-3.5 py-2 bg-white hover:bg-white/90 text-[#111] rounded-md text-[12px] font-medium cursor-pointer transition-colors border-none disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <IconZap /> {t('actions.open_best_account')}
          </button>
          <button
            onClick={onRefreshQuota}
            disabled={quotaRefreshing || refreshStatus?.refreshing}
            className="inline-flex items-center justify-center gap-1.5 px-3.5 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[12px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <IconRefresh /> {t('actions.refresh_quota')}
          </button>
          <button
            onClick={onAddAccount}
            className="inline-flex items-center justify-center gap-1.5 px-3.5 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[12px] font-medium cursor-pointer transition-colors border border-border"
          >
            <IconPlus /> {t('actions.add_account')}
          </button>
        </div>
      </div>

      {refreshStatus?.refreshing && (
        <div className="bg-notion-blue/10 border border-notion-blue/20 rounded-lg p-3 mb-5 flex items-center gap-3">
          <div className="w-4 h-4 border-2 border-notion-blue/30 border-t-notion-blue rounded-full animate-spin shrink-0" />
          <div className="flex-1 min-w-0">
            <div className="text-[12px] font-medium text-[#5c9ce6]">
              {t('common.status_refreshing', { current: refreshStatus.done, total: refreshStatus.total })}
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
      {!refreshStatus?.refreshing && (refreshStatus?.failed ?? 0) > 0 && (
        <div className="bg-warn/10 border border-warn/20 rounded-lg px-3 py-2.5 mb-5 text-[11px] text-warn">
          {t('common.status_refresh_failed', { count: refreshStatus?.failed ?? 0 })}
        </div>
      )}

      <section className="grid grid-cols-4 border-y border-white/[.06] divide-x divide-white/[.06] mb-6 max-lg:grid-cols-2 max-lg:[&>*:nth-child(3)]:border-t max-lg:[&>*:nth-child(4)]:border-t max-sm:grid-cols-2 max-sm:divide-x-0 max-sm:border-y-0 max-sm:gap-2 max-sm:[&>*]:rounded-lg max-sm:[&>*]:border max-sm:[&>*]:border-border max-sm:[&>*]:bg-bg-card">
        <StatCard
          label={t('dashboard.available_workspaces')}
          value={`${data.available} / ${data.total}`}
          sub={t('stats.ratio', { percent: summary.availableRate })}
          color="var(--color-ok)"
          icon={<IconShield />}
        />
        <StatCard
          label={t('dashboard.login_accounts')}
          value={loginCount}
          sub={t('dashboard.workspace_count', { count: data.total })}
        />
        <StatCard
          label={t('stats.basic_estimated')}
          value={fmt(summary.totalRemaining)}
          sub={summary.unlimitedAccounts > 0
            ? t('dashboard.limited_and_unlimited', { count: summary.unlimitedAccounts })
            : t('dashboard.limited_accounts_only')}
        />
        <StatCard
          label={t('dashboard.last_24h')}
          value={requestCount === undefined ? '—' : requestCount}
          sub={tokenStats
            ? t('dashboard.tokens_in_out', {
                input: formatTokens(tokenStats.last_24h.input),
                output: formatTokens(tokenStats.last_24h.output),
              })
            : t('stats.no_usage')}
          color="var(--color-notion-blue)"
          icon={<IconActivity />}
        />
      </section>

      <div className="grid grid-cols-[minmax(0,1.7fr)_minmax(280px,1fr)] gap-4 mb-4 max-lg:grid-cols-1">
        <section className="bg-bg-card border border-border rounded-lg p-5 max-sm:p-4">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-[14px] font-semibold text-text-primary flex items-center gap-2">
                <IconShield /> {t('dashboard.pool_health')}
              </h2>
              <p className="text-[11px] text-text-muted mt-1">{t('dashboard.pool_health_help')}</p>
            </div>
            <span className={`text-[11px] font-medium px-2 py-1 rounded border shrink-0 ${attentionTotal === 0 ? 'text-ok bg-ok/10 border-ok/20' : 'text-warn bg-warn/10 border-warn/20'}`}>
              {attentionTotal === 0 ? t('dashboard.healthy') : t('dashboard.attention_count', { count: attentionTotal })}
            </span>
          </div>
          <div className="flex h-2 rounded overflow-hidden bg-white/[.04] mt-5" aria-label={t('dashboard.pool_health')}>
            {healthSegments.map(segment => (
              <div
                key={segment.key}
                className={`${segment.color} min-w-[3px]`}
                style={{ width: `${data.total > 0 ? (segment.count / data.total) * 100 : 0}%` }}
                title={`${segment.label}: ${segment.count}`}
              />
            ))}
          </div>
          <div className="flex items-center gap-x-5 gap-y-2 flex-wrap mt-3">
            {healthSegments.map(segment => (
              <div key={segment.key} className="flex items-center gap-1.5 text-[11px] text-text-secondary">
                <span className={`w-1.5 h-1.5 rounded-full ${segment.color}`} />
                <span>{segment.label}</span>
                <strong className="text-text-primary tabular-nums">{segment.count}</strong>
              </div>
            ))}
          </div>
          <div className="mt-6 pt-5 border-t border-white/[.06]">
            <TotalQuotaBar summary={data.summary} />
          </div>
        </section>

        <section className="bg-bg-card border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-4 border-b border-white/[.06]">
            <h2 className="text-[14px] font-semibold text-text-primary">{t('dashboard.needs_attention')}</h2>
            <p className="text-[11px] text-text-muted mt-1">{t('dashboard.needs_attention_help')}</p>
          </div>
          {issues.length === 0 ? (
            <div className="px-4 py-8 text-center">
              <div className="w-8 h-8 rounded-full bg-ok/10 text-ok flex items-center justify-center mx-auto mb-3"><IconShield /></div>
              <div className="text-[12px] text-text-primary font-medium">{t('dashboard.no_attention')}</div>
              <div className="text-[11px] text-text-muted mt-1">{t('dashboard.no_attention_help')}</div>
            </div>
          ) : (
            <div className="divide-y divide-white/[.05]">
              {issues.map(issue => (
                <button
                  key={issue.status}
                  onClick={() => onShowAccounts(issue.status)}
                  className="w-full flex items-center gap-3 px-4 py-3 text-left bg-transparent hover:bg-white/[.025] cursor-pointer border-none transition-colors"
                >
                  <span className={`w-2 h-2 rounded-full shrink-0 ${issue.dot}`} />
                  <span className="min-w-0 flex-1">
                    <span className="block text-[12px] font-medium text-text-primary">{issue.label}</span>
                    <span className="block text-[10px] text-text-muted mt-0.5 truncate">{issue.detail}</span>
                  </span>
                  <span className="text-[12px] font-semibold text-text-primary tabular-nums">{issue.count}</span>
                  <span className="text-text-muted"><IconArrowRight /></span>
                </button>
              ))}
            </div>
          )}
          <button
            onClick={onManageAccounts}
            className="w-full flex items-center justify-between px-4 py-3 border-0 border-t border-white/[.06] bg-white/[.015] hover:bg-white/[.04] text-[11px] text-text-secondary hover:text-text-primary cursor-pointer transition-colors"
          >
            <span>{t('dashboard.manage_all_accounts')}</span><IconArrowRight />
          </button>
        </section>
      </div>

      <div className="grid grid-cols-[minmax(0,1.7fr)_minmax(280px,1fr)] gap-4 max-lg:grid-cols-1">
        <section className="bg-bg-card border border-border rounded-lg p-5 max-sm:p-4">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-[14px] font-semibold text-text-primary flex items-center gap-2"><IconActivity /> {t('dashboard.traffic')}</h2>
              <p className="text-[11px] text-text-muted mt-1">
                {tokenStats
                  ? t('dashboard.traffic_total', {
                      tokens: formatTokens(tokenStats.total.total),
                      requests: tokenStats.total.requests ?? 0,
                    })
                  : t('stats.no_usage')}
              </p>
            </div>
            <button onClick={onOpenHistory} className="inline-flex items-center gap-1.5 text-[11px] text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer">
              {t('api.request_history')} <IconArrowRight />
            </button>
          </div>
          {lastSevenDays.length > 1 ? (
            <div className="mt-5">
              <div className="h-28 flex items-end gap-2 border-b border-white/[.06]">
                {lastSevenDays.map(day => {
                  const totalHeight = Math.max(4, (day.total / maxDayTokens) * 100)
                  const outputShare = day.total > 0 ? (day.output / day.total) * 100 : 0
                  return (
                    <div key={day.date} className="flex-1 h-full flex items-end group">
                      <div
                        className="w-full bg-notion-blue/55 group-hover:bg-notion-blue/75 transition-colors rounded-t-sm overflow-hidden relative"
                        style={{ height: `${totalHeight}%` }}
                        title={`${day.date} · ${formatTokens(day.total)}`}
                      >
                        <div className="absolute bottom-0 inset-x-0 bg-[#9b72cf]/80" style={{ height: `${outputShare}%` }} />
                      </div>
                    </div>
                  )
                })}
              </div>
              <div className="flex gap-2 mt-2">
                {lastSevenDays.map(day => (
                  <div key={day.date} className="flex-1 text-center text-[9px] text-text-muted tabular-nums">
                    {new Date(`${day.date}T00:00:00`).toLocaleDateString(i18n.language === 'zh' ? 'zh-CN' : 'en-US', { month: 'numeric', day: 'numeric' })}
                  </div>
                ))}
              </div>
              <div className="flex items-center gap-4 mt-3 text-[10px] text-text-muted">
                <span className="flex items-center gap-1.5"><span className="w-2 h-2 rounded-sm bg-notion-blue/70" />{t('dashboard.input_tokens')}</span>
                <span className="flex items-center gap-1.5"><span className="w-2 h-2 rounded-sm bg-[#9b72cf]/80" />{t('dashboard.output_tokens')}</span>
              </div>
            </div>
          ) : lastSevenDays.length === 1 ? (
            <div className="mt-5 h-28 rounded-md border border-white/[.06] bg-black/10 flex items-center justify-center text-center px-4">
              <div>
                <div className="text-2xl font-semibold text-notion-blue tabular-nums">{formatTokens(lastSevenDays[0].total)}</div>
                <div className="text-[11px] text-text-secondary mt-1">
                  {new Date(`${lastSevenDays[0].date}T00:00:00`).toLocaleDateString(i18n.language === 'zh' ? 'zh-CN' : 'en-US', { month: 'long', day: 'numeric' })}
                </div>
                <div className="text-[10px] text-text-muted mt-2">
                  {t('dashboard.input_tokens')} {formatTokens(lastSevenDays[0].input)} · {t('dashboard.output_tokens')} {formatTokens(lastSevenDays[0].output)}
                </div>
              </div>
            </div>
          ) : (
            <div className="h-36 flex items-center justify-center text-[11px] text-text-muted">{t('dashboard.no_traffic')}</div>
          )}
        </section>

        <section className="bg-bg-card border border-border rounded-lg overflow-hidden">
          <div className="px-4 py-4 border-b border-white/[.06] flex items-start justify-between gap-3">
            <div>
              <h2 className="text-[14px] font-semibold text-text-primary">{t('dashboard.runtime')}</h2>
              <p className="text-[11px] text-text-muted mt-1">{t('dashboard.runtime_help')}</p>
            </div>
            <span className={`w-2 h-2 mt-1.5 rounded-full ${versionStatus?.status === 'update_available' ? 'bg-warn' : 'bg-ok'}`} />
          </div>
          <dl className="divide-y divide-white/[.05]">
            <div className="flex items-center justify-between gap-4 px-4 py-3">
              <dt className="text-[11px] text-text-muted">{t('dashboard.deployment')}</dt>
              <dd className="text-[11px] text-text-primary font-mono">{version}</dd>
            </div>
            <div className="flex items-center justify-between gap-4 px-4 py-3">
              <dt className="text-[11px] text-text-muted">{t('api.tool_bridge')}</dt>
              <dd className={`text-[11px] font-medium ${settings?.enable_tool_bridge ? 'text-ok' : 'text-text-secondary'}`}>
                {settings ? (settings.enable_tool_bridge ? t('api.enabled') : t('api.disabled')) : t('dashboard.unknown')}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-4 px-4 py-3">
              <dt className="text-[11px] text-text-muted">{t('api.tool_choice_policy')}</dt>
              <dd className="text-[11px] text-text-primary">{toolPolicy}</dd>
            </div>
            <div className="flex items-center justify-between gap-4 px-4 py-3">
              <dt className="text-[11px] text-text-muted">{t('dashboard.searches')}</dt>
              <dd className="text-[11px] text-text-primary">
                {settings
                  ? `${settings.enable_web_search ? t('dashboard.web') : '—'} · ${settings.enable_workspace_search ? t('dashboard.workspace') : '—'}`
                  : t('dashboard.unknown')}
              </dd>
            </div>
          </dl>
          <div className="px-4 py-3 border-t border-white/[.06] flex items-center justify-between gap-3">
            <span className="text-[10px] text-text-muted">
              {refreshTime ? t('actions.updated_at', { time: refreshTime }) : ''}
            </span>
            <div className="flex items-center gap-3">
              <button
                onClick={onRefreshData}
                disabled={refreshing}
                className={`text-text-muted hover:text-text-primary bg-transparent border-none cursor-pointer p-0.5 flex disabled:opacity-40 ${refreshing ? 'animate-spin' : ''}`}
                title={t('actions.refresh_data')}
              >
                <IconRefresh />
              </button>
              <button onClick={onOpenSettings} className="inline-flex items-center gap-1.5 text-[11px] text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer">
                {t('dashboard.open_settings')} <IconArrowRight />
              </button>
            </div>
          </div>
        </section>
      </div>
    </div>
  )
}

export default function App() {
  const { t, i18n } = useTranslation()
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
  const [selectedAccounts, setSelectedAccounts] = useState<Map<string, string>>(() => new Map())
  const [refreshStatus, setRefreshStatus] = useState<RefreshStatus | null>(null)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<AccountStatusFilter>('all')
  const [refreshTime, setRefreshTime] = useState('')
  const [page, setPage] = useState(0)
  const [settings, setSettings] = useState<SearchSettings | null>(null)
  const [tokenStats, setTokenStats] = useState<TokenStats | null>(null)
  const [apiKeyMasked, setApiKeyMasked] = useState('')
  const [apiKeyValue, setApiKeyValue] = useState('')
  const [apiKeyRevealed, setApiKeyRevealed] = useState(false)
  const [apiKeyLoading, setApiKeyLoading] = useState(false)
  const [apiKeyError, setApiKeyError] = useState<string | null>(null)
  const [versionStatus, setVersionStatus] = useState<DeploymentVersionStatus | null>(null)
  const [versionLoading, setVersionLoading] = useState(false)
  const [registerOpen, setRegisterOpen] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [requestHistoryOpen, setRequestHistoryOpen] = useState(false)
  const [copiedField, setCopiedField] = useState<'key' | 'base' | null>(null)
  const [showAddModal, setShowAddModal] = useState(false)
  const [showCleanupModal, setShowCleanupModal] = useState(false)
  const [activePage, setActivePage] = useState<DashboardPage>(dashboardPageFromHash)
  const appVersion = document.querySelector('meta[name="app-version"]')?.getAttribute('content') || 'dev'
  const copyToClipboard = async (text: string, field: 'key' | 'base') => {
    await navigator.clipboard.writeText(text)
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
  const [backupBusy, setBackupBusy] = useState<'download' | 'restore' | null>(null)
  const [backupNotice, setBackupNotice] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const backupFileInputRef = useRef<HTMLInputElement>(null)
  const PAGE_SIZE = 20
  const completedBatchJobsRef = useRef<Set<string>>(new Set())
  const loadDataGenerationRef = useRef(0)

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
    const syncPageFromHash = () => {
      const nextPage = dashboardPageFromHash()
      setActivePage(nextPage)
      if (nextPage === 'dashboard') {
        setQuery('')
        setDebouncedQuery('')
        setStatusFilter('all')
        setPage(0)
      }
    }
    window.addEventListener('hashchange', syncPageFromHash)
    return () => window.removeEventListener('hashchange', syncPageFromHash)
  }, [])

  useEffect(() => {
    if (activePage === 'settings') return
    setApiKeyRevealed(false)
    setApiKeyValue('')
  }, [activePage])

  const changePage = (nextPage: DashboardPage) => {
    if (nextPage === 'dashboard') {
      setQuery('')
      setDebouncedQuery('')
      setStatusFilter('all')
      setPage(0)
    }
    setActivePage(nextPage)
    const nextHash = `#${nextPage}`
    if (window.location.hash !== nextHash) window.history.replaceState(null, '', nextHash)
  }

  const showAccountsWithFilter = (status: AccountStatusFilter) => {
    setQuery('')
    setDebouncedQuery('')
    setStatusFilter(status)
    setPage(0)
    changePage('accounts')
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

  // Grouping is by login identity while the server stores one row per
  // workspace. Fetch all filtered workspace pages first so a login is never
  // split into duplicate cards at a server-page boundary.
  const loadData = useCallback(async () => {
    const generation = ++loadDataGenerationRef.current
    try {
      const d = await fetchAllDashboardData({ query: debouncedQuery, status: statusFilter })
      if (generation !== loadDataGenerationRef.current) return
      setData(d)
      setError(null)
      setRefreshTime(new Date().toLocaleTimeString('zh-CN'))
      if (d.refresh) {
        setRefreshStatus(d.refresh)
      }
    } catch (e: any) {
      if (generation !== loadDataGenerationRef.current) return
      setError(e.message || 'Unknown error')
    } finally {
      if (generation === loadDataGenerationRef.current) setLoading(false)
    }
  }, [debouncedQuery, statusFilter])

  useEffect(() => {
    if (authState === 'authenticated') loadData()
  }, [authState, loadData])

  // Health changes are produced by API traffic as well as manual quota
  // refreshes. Poll the in-memory admin snapshot while an operational page is
  // visible so a confirmed 401 appears promptly without calling Notion again.
  useEffect(() => {
    if (authState !== 'authenticated' || activePage === 'settings') return
    const interval = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !refreshStatus?.refreshing) void loadData()
    }, 10_000)
    return () => window.clearInterval(interval)
  }, [activePage, authState, loadData, refreshStatus?.refreshing])

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
    fetchAPIKey()
      .then(info => {
        setApiKeyMasked(info.masked || '')
        setApiKeyError(null)
      })
      .catch(error => setApiKeyError(error?.message || t('api.api_key_load_failed')))
    setVersionLoading(true)
    fetchVersionStatus()
      .then(setVersionStatus)
      .catch(() => setVersionStatus(null))
      .finally(() => setVersionLoading(false))
  }, [authState, t])

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
    if (['delete', 'delete_missing_personal_instructions', 'delete_exhausted', 'delete_no_workspace'].includes(activeBatchJob.action)) {
      setSelectedAccounts(new Map())
    }
    loadData()
  }, [activeBatchJob, loadData])

  const handleLogout = async () => {
    await logout()
    setApiKeyValue('')
    setApiKeyMasked('')
    setApiKeyRevealed(false)
    setAuthState('login')
    setData(null)
  }

  const toggleAPIKey = async () => {
    if (apiKeyRevealed) {
      setApiKeyRevealed(false)
      setApiKeyValue('')
      return
    }
    setApiKeyLoading(true)
    setApiKeyError(null)
    try {
      const info = await fetchAPIKey(true)
      if (!info.value) throw new Error(t('api.api_key_load_failed'))
      setApiKeyMasked(info.masked || apiKeyMasked)
      setApiKeyValue(info.value)
      setApiKeyRevealed(true)
    } catch (error: any) {
      setApiKeyError(error?.message || t('api.api_key_load_failed'))
    } finally {
      setApiKeyLoading(false)
    }
  }

  const copyAPIKey = async () => {
    setApiKeyLoading(true)
    setApiKeyError(null)
    try {
      const value = apiKeyValue || (await fetchAPIKey(true)).value
      if (!value) throw new Error(t('api.api_key_load_failed'))
      await copyToClipboard(value, 'key')
    } catch (error: any) {
      setApiKeyError(error?.message || t('api.api_key_load_failed'))
    } finally {
      setApiKeyLoading(false)
    }
  }

  const refreshVersionStatus = async () => {
    setVersionLoading(true)
    try {
      setVersionStatus(await fetchVersionStatus(true))
    } catch {
      setVersionStatus(null)
    } finally {
      setVersionLoading(false)
    }
  }

  const refresh = async () => {
    setRefreshing(true)
    await Promise.all([
      loadData(),
      fetchTokenStats().then(setTokenStats).catch(() => {}),
    ])
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

  const launchAccountBatch = async (
    action: AccountBatchJobAction,
    accountIds: string[],
    legacyEmails: string[] = [],
  ): Promise<boolean> => {
    if ((accountIds.length === 0 && legacyEmails.length === 0) || batchStartingAction || activeBatchJob?.state === 'running') return false
    setBatchStartingAction(action)
    try {
      const job = await startAccountBatchJob(action, accountIds, 10, legacyEmails)
      setActiveBatchJob(job)
      window.localStorage.setItem('notion-manager-active-account-batch-job', job.id)
      return true
    } catch (e: any) {
      window.alert(t('batch.start_failed', { error: e?.message || t('common.request_failed') }))
      return false
    } finally {
      setBatchStartingAction(null)
    }
  }

  const launchAllPoolBatch = async (action: AccountBatchJobAction): Promise<boolean> => {
    try {
      const accounts = await fetchAccountSelection('', 'all')
      const accountIds = accounts.flatMap(account => account.account_id ? [account.account_id] : [])
      const legacyEmails = accounts.flatMap(account => !account.account_id && account.email ? [account.email] : [])
      return await launchAccountBatch(action, accountIds, legacyEmails)
    } catch (e: any) {
      window.alert(t('batch.fetch_all_failed', { error: e?.message || t('common.request_failed') }))
      return false
    }
  }

  const handleCheckPersonalInstructions = async () => {
    if ((data?.total ?? 0) === 0) return
    await launchAllPoolBatch('check_personal_instructions')
  }

  const handleDeleteMissingPersonalInstructions = async (): Promise<boolean> => {
    if ((data?.total ?? 0) === 0 || activeBatchJob?.state === 'running') return false
    const knownMissing = data?.summary?.personal_instructions_missing ?? 0
    const confirmed = window.confirm(
      t('batch.confirm_delete_missing', { total: data?.total ?? 0, missing: knownMissing }),
    )
    if (!confirmed) return false
    return await launchAllPoolBatch('delete_missing_personal_instructions')
  }

  const handleBulkSelected = async (action: Extract<AccountBatchJobAction, 'delete' | 'disable' | 'enable' | 'check_personal_instructions'>) => {
    const entries = Array.from(selectedAccounts.entries())
    const accountIds = entries.flatMap(([selector]) => selector.startsWith('legacy:') ? [] : [selector])
    const legacyEmails = entries.flatMap(([selector, email]) => selector.startsWith('legacy:') && email ? [email] : [])
    if (accountIds.length === 0 && legacyEmails.length === 0) return
    if (action === 'delete') {
      const confirmed = window.confirm(
        t('batch.confirm_delete_selected', { count: accountIds.length || legacyEmails.length }),
      )
      if (!confirmed) return
    }
    await launchAccountBatch(action, accountIds, legacyEmails)
  }

  const handleSelectAllResults = async () => {
    if (selectingAllResults) return
    setSelectingAllResults(true)
    try {
      const accounts = await fetchAccountSelection(debouncedQuery, statusFilter)
      const selections: Array<[string, string]> = []
      accounts.forEach(account => {
        if (account.account_id) {
          selections.push([account.account_id, account.email])
          return
        }
        const email = account.email?.trim()
        if (email) selections.push([`legacy:${email.toLowerCase()}`, email])
      })
      setSelectedAccounts(new Map(selections))
    } catch (e: any) {
      window.alert(t('common.select_all_failed', { error: e?.message || t('common.request_failed') }))
    } finally {
      setSelectingAllResults(false)
    }
  }

  const copySelectedEmails = async () => {
    const emails = Array.from(new Set(Array.from(selectedAccounts.values()).filter(Boolean)))
    if (emails.length === 0) return
    try {
      await navigator.clipboard.writeText(emails.join('\n'))
      window.alert(t('common.copy_emails_success', { count: emails.length }))
    } catch {
      window.alert(t('common.copy_failed'))
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
      window.alert(t('batch.retry_action_failed', { error: e?.message || t('common.request_failed') }))
    } finally {
      setBatchStartingAction(null)
    }
  }

  const closeActiveBatch = () => {
    if (activeBatchJob?.state === 'running') return
    setActiveBatchJob(null)
    window.localStorage.removeItem('notion-manager-active-account-batch-job')
  }

  const handleDeleteExhaustedTrials = async (): Promise<boolean> => {
    const count = data?.summary?.exhausted_trials ?? 0
    if (count <= 0 || activeBatchJob?.state === 'running') return false
    const confirmed = window.confirm(
      t('batch.confirm_delete_exhausted', { count }),
    )
    if (!confirmed) return false
    return await launchAllPoolBatch('delete_exhausted')
  }

  const handleDeleteFilteredAccounts = async (
    status: Extract<AccountStatusFilter, 'auth_invalid' | 'no_workspace'>,
    count: number,
    action: Extract<AccountBatchJobAction, 'delete' | 'delete_no_workspace'>,
  ): Promise<boolean> => {
    if (count <= 0 || activeBatchJob?.state === 'running') return false
    const confirmed = window.confirm(t('cleanup.confirm', {
      type: status === 'auth_invalid' ? t('cleanup.auth_invalid') : t('cleanup.no_workspace'),
      count,
    }))
    if (!confirmed) return false
    try {
      const accounts = await fetchAccountSelection('', status)
      const accountIds = accounts.flatMap(account => account.account_id ? [account.account_id] : [])
      const legacyEmails = accounts.flatMap(account => !account.account_id && account.email ? [account.email] : [])
      return await launchAccountBatch(action, accountIds, legacyEmails)
    } catch (e: any) {
      window.alert(t('batch.fetch_all_failed', { error: e?.message || t('common.request_failed') }))
      return false
    }
  }

  const handleCleanupTarget = async (target: CleanupTarget) => {
    let started = false
    switch (target) {
      case 'auth_invalid':
        started = await handleDeleteFilteredAccounts('auth_invalid', summary?.authInvalid ?? 0, 'delete')
        break
      case 'no_workspace':
        started = await handleDeleteFilteredAccounts('no_workspace', summary?.noWorkspace ?? 0, 'delete_no_workspace')
        break
      case 'exhausted':
        started = await handleDeleteExhaustedTrials()
        break
      case 'personal_missing':
        started = await handleDeleteMissingPersonalInstructions()
        break
    }
    if (started) setShowCleanupModal(false)
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

  const setToolChoicePolicy = async (policy: ToolChoicePolicy) => {
    if (!settings || promptModeSaving || settings.tool_choice_policy === policy) return
    setPromptModeSaving(true)
    try {
      const updated = await updateSettings({ tool_choice_policy: policy })
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
      setProxyError(e?.message || t('api.save_failed'))
      setProxyDraft(settings.notion_proxy ?? '')
    } finally {
      setProxySaving(false)
    }
  }

  const handleDownloadBackup = async () => {
    if (backupBusy) return
    setBackupBusy('download')
    setBackupNotice(null)
    try {
      const { blob, filename } = await downloadBackup()
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filename
      document.body.appendChild(link)
      link.click()
      link.remove()
      URL.revokeObjectURL(url)
      setBackupNotice({ type: 'success', text: t('api.backup_downloaded') })
    } catch (e: any) {
      setBackupNotice({ type: 'error', text: e?.message || t('api.backup_failed') })
    } finally {
      setBackupBusy(null)
    }
  }

  const handleRestoreBackup = async (file: File) => {
    if (backupBusy) return
    const confirmed = window.confirm(t('api.restore_confirm'))
    if (!confirmed) return
    setBackupBusy('restore')
    setBackupNotice(null)
    try {
      const result = await restoreBackup(file)
      setSettings(result.settings)
      setProxyDraft(result.settings.notion_proxy ?? '')
      setSelectedAccounts(new Map())
      setPage(0)
      await loadData()
      setBackupNotice({ type: 'success', text: t('api.restore_success', { count: result.accounts }) })
    } catch (e: any) {
      setBackupNotice({ type: 'error', text: e?.message || t('api.restore_failed') })
    } finally {
      setBackupBusy(null)
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

  const accounts = data?.accounts || []
  const accountGroups = useMemo(() => groupWorkspaceAccounts(accounts), [accounts])
  const filteredWorkspaceTotal = data?.filtered_total ?? accounts.length
  const filteredTotal = accountGroups.length
  const totalPages = Math.max(1, Math.ceil(filteredTotal / PAGE_SIZE))
  const pagedGroups = accountGroups.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)
  const pageAccountIDs = pagedGroups.flatMap(group => group.accounts.map(account => account.account_id))
  const selectedOnPage = pageAccountIDs.filter(accountID => selectedAccounts.has(accountID)).length
  const allPageSelected = pageAccountIDs.length > 0 && selectedOnPage === pageAccountIDs.length
  const batchBusy = !!batchStartingAction || activeBatchJob?.state === 'running'

  const toggleSelectedAccount = (account: AccountInfo) => {
    const accountID = account.account_id
    if (!accountID) return
    setSelectedAccounts(current => {
      const next = new Map(current)
      if (next.has(accountID)) next.delete(accountID)
      else next.set(accountID, account.email)
      return next
    })
  }

  const toggleCurrentPageSelection = () => {
    setSelectedAccounts(current => {
      const next = new Map(current)
      if (allPageSelected) {
        pagedGroups.forEach(group => group.accounts.forEach(account => next.delete(account.account_id)))
      } else {
        pagedGroups.forEach(group => group.accounts.forEach(account => {
          if (account.account_id) next.set(account.account_id, account.email)
        }))
      }
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

  const summary = useMemo<PoolSummaryView | null>(() => {
    if (!data) return null
    const s = data.summary
    // Note: backend's AvailableCount already excludes no_workspace, so
    // (total - available) lumps "exhausted" and "no workspace" together.
    // We split them out explicitly for the operator.
    const exhausted = data.total - data.available
    const exhaustedOnly = s?.exhausted_only ?? 0
    const noWorkspace = s?.no_workspace ?? 0
    const aiDisabled = s?.ai_disabled ?? 0
    const authInvalid = s?.auth_invalid ?? 0
    const disabled = s?.disabled ?? 0
    const otherUnavailable = Math.max(0, exhausted - exhaustedOnly - noWorkspace - aiDisabled - authInvalid - disabled)
    const availableRate = data.total > 0 ? Math.round((data.available / data.total) * 100) : 0
    const sameBasicQuota = isSameQuota(
      { usage: s?.total_space_usage ?? 0, limit: s?.total_space_limit ?? 0 },
      { usage: s?.total_user_usage ?? 0, limit: s?.total_user_limit ?? 0 },
    )
    return {
      exhausted,
      exhaustedOnly,
      noWorkspace,
      aiDisabled,
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
      unlimitedAccounts: s?.unlimited_accounts ?? 0,
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
        {t('common.loading_accounts')}
      </div>
    )
  }

  if (error && !data) {
    return (
      <div className="flex items-center justify-center h-screen text-err text-sm">
        {t('common.load_failed', { error })}
      </div>
    )
  }

  const accountSummaryParts = summary
    ? [
      t('stats.available_count', { count: data!.available }),
      summary.exhaustedOnly > 0 ? t('stats.exhausted_count', { count: summary.exhaustedOnly }) : null,
      summary.noWorkspace > 0 ? t('stats.no_workspace_count', { count: summary.noWorkspace }) : null,
      summary.aiDisabled > 0 ? t('stats.ai_disabled_count', { count: summary.aiDisabled }) : null,
      summary.authInvalid > 0 ? t('stats.auth_invalid_count', { count: summary.authInvalid }) : null,
      summary.disabled > 0 ? t('stats.disabled_count', { count: summary.disabled }) : null,
      summary.otherUnavailable > 0 ? t('stats.temporary_count', { count: summary.otherUnavailable }) : null,
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
        {activePage === 'dashboard' && summary && data && (
          <DashboardHome
            data={data}
            summary={summary}
            loginCount={accountGroups.length}
            tokenStats={tokenStats}
            settings={settings}
            versionStatus={versionStatus}
            appVersion={appVersion}
            refreshStatus={refreshStatus}
            refreshing={refreshing}
            quotaRefreshing={quotaRefreshing}
            refreshTime={refreshTime}
            onOpenBest={openBestProxy}
            onRefreshQuota={handleQuotaRefresh}
            onRefreshData={refresh}
            onAddAccount={() => setShowAddModal(true)}
            onManageAccounts={() => changePage('accounts')}
            onShowAccounts={showAccountsWithFilter}
            onOpenHistory={() => setRequestHistoryOpen(true)}
            onOpenSettings={() => changePage('settings')}
          />
        )}

        {activePage === 'accounts' && data && (
          <div className="hidden max-sm:block mb-4">
            <h1 className="text-[19px] font-semibold text-text-primary">{t('common.accounts_pool')}</h1>
            <p className="text-[11px] text-text-muted mt-1">
              {t('common.accounts_page_summary', { logins: accountGroups.length, workspaces: data.total })}
            </p>
          </div>
        )}

        {/* Summary */}
        {activePage === 'accounts' && summary && (
          <div className="grid grid-cols-5 divide-x divide-white/[.05] mb-6 max-lg:grid-cols-3 max-md:grid-cols-2 max-md:divide-x-0 max-sm:gap-2 max-sm:mb-3 max-sm:[&>*]:rounded-lg max-sm:[&>*]:border max-sm:[&>*]:border-border max-sm:[&>*]:bg-bg-card max-sm:[&>*:last-child]:col-span-2">
            <StatCard
              label={t('stats.total_accounts')} value={data!.total}
              sub={accountSummaryParts}
            />
            <StatCard
              label={t('stats.available')} value={data!.available}
              sub={t('stats.ratio', { percent: summary.availableRate })}
              color="var(--color-ok)"
            />
            <StatCard
              label={t('stats.basic_estimated')} value={fmt(summary.totalRemaining)}
              sub={summary.unlimitedAccounts > 0
                ? summary.sameBasicQuota
                  ? t('stats.basic_same_with_unlimited', { count: summary.unlimitedAccounts })
                  : t('stats.basic_split_with_unlimited', { space: fmt(summary.totalSpaceRemaining), user: fmt(summary.totalUserRemaining), count: summary.unlimitedAccounts })
                : summary.sameBasicQuota
                  ? t('stats.basic_same')
                  : t('stats.basic_split', { space: fmt(summary.totalSpaceRemaining), user: fmt(summary.totalUserRemaining) })}
            />
            <StatCard
              label={t('stats.premium_raw')} value={fmt(summary.totalPremiumBalance)}
              sub={summary.totalPremiumLimit > 0
                ? t('stats.premium_signal', { count: summary.premiumAccounts, usage: summary.totalResearchUsage })
                : t('stats.premium_empty')}
              color="var(--color-research, #9b51e0)"
            />
            <StatCard
              icon={<IconActivity />}
              label={t('stats.token_usage')}
              value={formatTokens(tokenStats?.total.total ?? 0)}
              sub={tokenStats
                ? t('stats.today_usage', {
                    today: formatTokens(tokenStats.today.total),
                    input: formatTokens(tokenStats.today.input),
                    output: formatTokens(tokenStats.today.output),
                  })
                : t('stats.no_usage')}
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
                {t('common.status_refreshing', { current: refreshStatus.done, total: refreshStatus.total })}
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
        {activePage === 'accounts' && !refreshStatus?.refreshing && (refreshStatus?.failed ?? 0) > 0 && (
          <div className="bg-warn/10 border border-warn/20 rounded-lg px-3 py-2.5 mb-5 text-[12px] text-warn">
            {t('common.status_refresh_failed', { count: refreshStatus?.failed ?? 0 })}
          </div>
        )}

        {/* Actions */}
        {activePage === 'accounts' && <div className="flex items-center gap-2.5 mb-5 flex-wrap max-sm:grid max-sm:grid-cols-2 max-sm:gap-2 max-sm:[&>button]:w-full max-sm:[&>button]:justify-center max-sm:[&>button]:px-3 max-sm:[&>button]:text-[12px] max-sm:[&>button:last-of-type]:col-span-2">
          <button
            onClick={openBestProxy}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-white hover:bg-white/90 text-[#111] rounded-md text-[13px] font-medium cursor-pointer transition-colors border-none"
          >
            <IconZap /> {t('actions.open_best_account')}
          </button>
          <button
            onClick={handleQuotaRefresh}
            disabled={quotaRefreshing || refreshStatus?.refreshing}
            className={`inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed ${refreshStatus?.refreshing ? 'animate-pulse' : ''}`}
          >
            <IconRefresh /> {t('actions.refresh_quota')}
          </button>
          <button
            onClick={refresh}
            disabled={refreshing}
            className={`inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed ${refreshing ? 'animate-pulse' : ''}`}
          >
            <IconRefresh /> {t('actions.refresh_data')}
          </button>
          <button
            onClick={handleCheckPersonalInstructions}
            disabled={batchBusy || (data?.total ?? 0) === 0}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border disabled:opacity-50 disabled:cursor-not-allowed"
            title={t('actions.check_instructions_help')}
          >
            <IconActivity /> {batchStartingAction === 'check_personal_instructions' ? t('actions.starting') : t('actions.check_instructions')}
          </button>
          <button
            onClick={() => setShowCleanupModal(true)}
            disabled={batchBusy || (data?.total ?? 0) === 0 || !!refreshStatus?.refreshing}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-err/10 hover:bg-err/20 text-err rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
            title={t('cleanup.subtitle')}
          >
            <IconTrash /> {t('cleanup.button')}
          </button>
          <button
            onClick={() => setShowAddModal(true)}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border"
          >
            <IconPlus /> {t('actions.add_account')}
          </button>
          <button
            onClick={() => setRegisterOpen(true)}
            className="inline-flex items-center gap-1.5 px-4 py-2 bg-bg-card hover:bg-bg-card-hover text-text-primary rounded-md text-[13px] font-medium cursor-pointer transition-colors border border-border"
          >
            <IconUserPlus size={13} /> {t('actions.register_account')}
          </button>
          {refreshTime && (
            <span className="text-[11px] text-text-muted max-sm:hidden">
              {t('actions.updated_at', { time: refreshTime })}
              {refreshStatus?.last_refresh_at && !refreshStatus.refreshing && (
                <> · {t('actions.quota_refreshed_at', { time: new Date(refreshStatus.last_refresh_at).toLocaleTimeString(i18n.language === 'zh' ? 'zh-CN' : 'en-US') })}</>
              )}
            </span>
          )}
        </div>}

        {activePage === 'settings' && (
          <div className="mb-6">
            <div className="mb-5">
              <h2 className="text-[18px] font-semibold text-text-primary">{t('api.settings_and_history')}</h2>
              <p className="text-[12px] text-text-muted mt-1">{t('api.settings_description')}</p>
            </div>
            <div className="grid grid-cols-4 gap-3 max-xl:grid-cols-2 max-sm:grid-cols-2 max-sm:gap-2">
              <div className="bg-bg-card border border-border rounded-lg p-4 max-sm:p-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="text-[11px] text-text-muted uppercase tracking-wider">{t('api.deployment_version')}</div>
                  <button
                    type="button"
                    onClick={refreshVersionStatus}
                    disabled={versionLoading}
                    className={`text-text-muted hover:text-text-primary bg-transparent border-none cursor-pointer p-0.5 flex disabled:opacity-40 ${versionLoading ? 'animate-spin' : ''}`}
                    title={t('api.check_version')}
                  >
                    <IconRefresh />
                  </button>
                </div>
                <div className="flex items-baseline gap-2 mt-1">
                  <div className="text-[20px] font-semibold font-mono" title={versionStatus?.current_version || appVersion}>{displayVersion(versionStatus?.current_version || appVersion)}</div>
                  {versionStatus && (
                    <span className={`text-[10px] ${versionStatus.status === 'up_to_date' ? 'text-ok' : versionStatus.status === 'update_available' ? 'text-warn' : 'text-text-muted'}`}>
                      {t(`api.version_${versionStatus.status}`, { defaultValue: t('api.version_unknown') })}
                    </span>
                  )}
                </div>
                <div className="text-[11px] text-text-muted mt-1 font-mono" title={versionStatus?.latest_version || ''}>
                  {t('api.latest_image')}: {versionStatus?.latest_version ? displayVersion(versionStatus.latest_version) : t('api.version_unknown')}
                </div>
                {versionStatus?.error && <div className="text-[10px] text-warn mt-1 truncate" title={versionStatus.error}>{t('api.version_check_failed')}</div>}
              </div>
              <button
                onClick={() => setRequestHistoryOpen(true)}
                className="text-left bg-bg-card hover:bg-bg-card-hover border border-border rounded-lg p-4 cursor-pointer transition-colors max-sm:p-3"
              >
                <div className="flex items-center gap-2 text-[13px] font-medium"><IconActivity /> {t('api.request_history')}</div>
                <div className="text-[11px] text-text-muted mt-2">{t('api.request_history_help')}</div>
              </button>
              <button
                onClick={() => setHistoryOpen(true)}
                className="text-left bg-bg-card hover:bg-bg-card-hover border border-border rounded-lg p-4 cursor-pointer transition-colors max-sm:col-span-2 max-sm:p-3"
              >
                <div className="flex items-center gap-2 text-[13px] font-medium"><IconHistory size={13} /> {t('api.register_history')}</div>
                <div className="text-[11px] text-text-muted mt-2">{t('api.register_history_help')}</div>
              </button>
              <div className="bg-bg-card border border-border rounded-lg p-4 min-w-0 max-sm:col-span-2 max-sm:p-3">
                <div className="flex items-center gap-2 text-[13px] font-medium"><IconDatabase size={14} /> {t('api.data_backup')}</div>
                <div className="text-[11px] text-text-muted mt-2 leading-relaxed">{t('api.data_backup_help')}</div>
                <div className="text-[10px] text-warn mt-2">{t('api.data_backup_warning')}</div>
                <div className="grid grid-cols-2 gap-2 mt-3">
                  <button
                    type="button"
                    onClick={handleDownloadBackup}
                    disabled={backupBusy !== null}
                    className="h-8 inline-flex items-center justify-center gap-1.5 rounded-md border border-border bg-bg-secondary hover:bg-bg-card-hover text-[11px] text-text-primary cursor-pointer disabled:opacity-40 disabled:cursor-wait"
                  >
                    <IconDownload size={13} /> {backupBusy === 'download' ? t('api.backup_downloading') : t('api.download_backup')}
                  </button>
                  <button
                    type="button"
                    onClick={() => backupFileInputRef.current?.click()}
                    disabled={backupBusy !== null}
                    className="h-8 inline-flex items-center justify-center gap-1.5 rounded-md border border-notion-blue/30 bg-notion-blue/10 hover:bg-notion-blue/20 text-[11px] text-notion-blue cursor-pointer disabled:opacity-40 disabled:cursor-wait"
                  >
                    <IconUpload size={13} /> {backupBusy === 'restore' ? t('api.backup_restoring') : t('api.restore_backup')}
                  </button>
                  <input
                    ref={backupFileInputRef}
                    type="file"
                    accept="application/json,.json"
                    className="hidden"
                    onChange={event => {
                      const file = event.target.files?.[0]
                      event.target.value = ''
                      if (file) void handleRestoreBackup(file)
                    }}
                  />
                </div>
                {backupNotice && (
                  <div className={`text-[10px] mt-2 break-words ${backupNotice.type === 'success' ? 'text-ok' : 'text-err'}`}>
                    {backupNotice.text}
                  </div>
                )}
              </div>
            </div>
          </div>
        )}

        {/* API Settings */}
        {activePage === 'settings' && settings && (() => {
          const apiBase = `${window.location.origin}/v1`
          return (
            <div className="mb-6 px-5 py-5 bg-[#171717] border border-white/5 rounded-lg shadow-inner max-sm:px-3 max-sm:py-4">
              <div className="mb-4">
                <div className="text-[14px] text-text-primary font-semibold flex items-center gap-2"><IconSettings /> {t('api.feature_settings')}</div>
                <div className="text-[11px] text-text-muted mt-1">{t('api.persist_help')}</div>
              </div>
              <div className="mb-5 rounded-lg border border-white/[.07] bg-white/[.025] p-3.5">
                <div className="flex items-center justify-between gap-3 mb-3 max-sm:items-start">
                  <div>
                    <div className="text-[13px] font-medium text-text-primary">{t('api.prompt_tools')}</div>
                    <div className="text-[11px] text-text-muted mt-0.5">{t('api.prompt_tools_help')}</div>
                  </div>
                  <span className="text-[10px] text-ok bg-ok/10 border border-ok/20 rounded px-2 py-0.5 shrink-0">
                    {promptModeSaving ? t('api.saving') : t('api.saved')}
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
                      <span className="text-[12px] font-semibold text-text-primary">{t('api.client_prompt')}</span>
                      <span className={`text-[10px] ${settings.use_client_system_prompt ? 'text-ok' : 'text-text-muted'}`}>{settings.use_client_system_prompt ? t('api.enabled') : t('api.disabled')}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">{t('api.client_prompt_help')}</div>
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
                      <span className="text-[12px] font-semibold text-text-primary">{t('api.notion_personal')}</span>
                      <span className={`text-[10px] ${settings.use_notion_personal_instructions ? 'text-ok' : 'text-text-muted'}`}>{settings.use_notion_personal_instructions ? t('api.enabled') : t('api.disabled')}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">{t('api.notion_personal_help')}</div>
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
                      <span className="text-[12px] font-semibold text-text-primary">{t('api.tool_bridge')}</span>
                      <span className={`text-[10px] ${settings.enable_tool_bridge ? 'text-ok' : 'text-text-muted'}`}>{settings.enable_tool_bridge ? t('api.enabled') : t('api.disabled')}</span>
                    </div>
                    <div className="text-[11px] text-text-muted mt-1.5 leading-relaxed">{t('api.tool_bridge_help')}</div>
                  </button>
                </div>
                <div className="mt-3 pt-3 border-t border-white/[.06] flex items-center justify-between gap-4 max-md:flex-col max-md:items-stretch">
                  <div className="min-w-0">
                    <div className="text-[12px] font-semibold text-text-primary">{t('api.tool_choice_policy')}</div>
                    <div className="text-[11px] text-text-muted mt-0.5 leading-relaxed">{t('api.tool_choice_policy_help')}</div>
                  </div>
                  <div className="grid grid-cols-4 p-0.5 rounded-md border border-white/[.07] bg-black/20 shrink-0 max-sm:grid-cols-2" role="radiogroup" aria-label={t('api.tool_choice_policy')}>
                    {(['client', 'auto', 'required', 'none'] as ToolChoicePolicy[]).map(policy => (
                      <button
                        key={policy}
                        type="button"
                        role="radio"
                        aria-checked={settings.tool_choice_policy === policy}
                        disabled={promptModeSaving}
                        onClick={() => setToolChoicePolicy(policy)}
                        className={`h-8 min-w-[76px] px-2 rounded text-[11px] font-medium cursor-pointer transition-colors disabled:cursor-wait ${settings.tool_choice_policy === policy ? 'bg-white/10 text-white shadow-sm' : 'bg-transparent text-text-muted hover:text-text-primary'}`}
                      >
                        {t(`api.tool_choice_${policy}`)}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
              <div className="flex items-center gap-6 flex-wrap max-sm:flex-col max-sm:items-stretch max-sm:gap-4">
                <div className="flex items-center gap-6 flex-wrap max-sm:flex-col max-sm:items-stretch max-sm:gap-3 max-sm:w-full">
                  <div className="flex items-center gap-1.5 max-sm:flex-wrap">
                    <span className="text-[11px] text-text-muted">API Key</span>
                    <code
                      className={`text-[11px] bg-white/[.05] px-1.5 py-0.5 rounded cursor-pointer hover:bg-white/[.1] transition-colors font-mono max-sm:max-w-[220px] max-sm:truncate ${copiedField === 'key' ? 'text-ok' : 'text-text-primary'}`}
                      onClick={copyAPIKey}
                      title={t('api.click_to_copy')}
                    >
                      {copiedField === 'key' ? `✓ ${t('api.copied')}` : apiKeyLoading ? t('api.loading_key') : (apiKeyRevealed ? apiKeyValue : apiKeyMasked || t('api.key_unavailable'))}
                    </code>
                    <button
                      onClick={toggleAPIKey}
                      disabled={apiKeyLoading}
                      className="ml-3 text-text-muted hover:text-text-primary transition-colors bg-transparent border-none cursor-pointer px-0.5 flex items-center"
                      title={apiKeyRevealed ? t('api.hide') : t('api.show')}
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
                      title={t('api.click_to_copy')}
                    >
                      {copiedField === 'base' ? `✓ ${t('api.copied')}` : apiBase}
                    </code>
                  </div>
                  <div className="flex items-center gap-1.5 max-sm:grid max-sm:grid-cols-[auto_8px_1fr]">
                    <span className="text-[11px] text-text-muted">{t('api.global_proxy')}</span>
                    <span
                      className={`inline-block w-1.5 h-1.5 rounded-full ${proxyError ? 'bg-err' : settings.notion_proxy ? 'bg-ok' : 'bg-text-muted/60'}`}
                      title={proxyError ? proxyError : settings.notion_proxy ? t('api.proxy_enabled') : t('api.direct_connection')}
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
                      placeholder={t('api.direct_connection_tip')}
                      disabled={proxySaving}
                      className={`text-[11px] bg-white/[.05] px-1.5 py-0.5 rounded font-mono outline-none border w-[160px] focus:w-[280px] transition-[width,border-color] duration-150 max-sm:w-full max-sm:focus:w-full ${proxyError ? 'border-err text-err' : 'border-transparent focus:border-white/20 text-text-primary'} placeholder:text-text-muted/60`}
                      title={proxyError || (settings.notion_proxy ? t('api.current_proxy', { proxy: settings.notion_proxy }) : t('api.current_direct'))}
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
                    <span className="text-[12px] text-white font-medium">{t('api.web_search')}</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <button
                      onClick={() => toggleSetting('enable_workspace_search')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.enable_workspace_search ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.enable_workspace_search ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-text-primary">{t('api.workspace_search')}</span>
                  </label>
                  <label
                    className="flex items-center gap-2 cursor-pointer select-none"
                    title={t('api.ask_help')}
                  >
                    <button
                      onClick={() => toggleSetting('ask_mode_default')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.ask_mode_default ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.ask_mode_default ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    {apiKeyError && <span className="text-[10px] text-err" title={apiKeyError}>{t('api.api_key_load_failed')}</span>}
                    <span className="text-[12px] text-text-primary">{t('api.ask_mode')}</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <button
                      onClick={() => toggleSetting('debug_logging')}
                      className={`relative w-7 h-4 rounded-full transition-colors duration-200 cursor-pointer border-none ${settings.debug_logging ? 'bg-[#4dab9a]' : 'bg-white/10 border border-white/5'}`}
                    >
                      <span className={`absolute top-[2px] left-[2px] w-3 h-3 rounded-full transition-all duration-200 ${settings.debug_logging ? 'bg-white shadow-sm translate-x-[12px]' : 'bg-white/40'}`} />
                    </button>
                    <span className="text-[12px] text-text-primary">{t('api.debug_log')}</span>
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
            title={t('common.filter_status')}
          >
            {accountStatusFilterOptions.map(option => <option key={option.value} value={option.value}>{t(option.labelKey)}</option>)}
          </select>
          <button
            onClick={toggleCurrentPageSelection}
            disabled={pageAccountIDs.length === 0 || batchBusy}
            className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-primary rounded-md text-[12px] font-medium cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {allPageSelected ? t('common.unselect_page') : t('common.select_page')}
          </button>
          <button
            onClick={handleSelectAllResults}
            disabled={filteredTotal === 0 || batchBusy || selectingAllResults}
            className="px-3 py-1.5 bg-notion-blue/10 hover:bg-notion-blue/20 text-notion-blue rounded-md text-[12px] font-medium cursor-pointer border border-notion-blue/25 disabled:opacity-40 disabled:cursor-not-allowed"
            title={t('common.select_all_help')}
          >
            {selectingAllResults ? t('common.selecting_all') : t('common.select_all', { count: filteredWorkspaceTotal })}
          </button>
          <span className="text-[12px] text-text-secondary mr-auto self-center">
            {t('common.selected', { count: selectedAccounts.size })}
            {selectedOnPage > 0 && selectedAccounts.size !== selectedOnPage ? t('common.selected_page', { count: selectedOnPage }) : ''}
          </span>
          {selectedAccounts.size > 0 && <div className="flex items-center gap-2 flex-wrap max-sm:grid max-sm:grid-cols-2 max-sm:w-full">
            <button
              onClick={copySelectedEmails}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-secondary hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {t('common.copy_emails')}
            </button>
            <button
              onClick={() => handleBulkSelected('check_personal_instructions')}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-bg-secondary hover:bg-bg-card-hover text-text-secondary hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'check_personal_instructions' ? t('actions.starting') : t('common.check_selected')}
            </button>
            <button
              onClick={() => handleBulkSelected('disable')}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-warn/10 hover:bg-warn/20 text-warn rounded-md text-[12px] cursor-pointer border border-warn/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'disable' ? t('actions.starting') : t('common.disable_selected')}
            </button>
            <button
              onClick={() => handleBulkSelected('enable')}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-ok/10 hover:bg-ok/20 text-ok rounded-md text-[12px] cursor-pointer border border-ok/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'enable' ? t('actions.starting') : t('common.enable_selected')}
            </button>
            <button
              onClick={() => handleBulkSelected('delete')}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-err/10 hover:bg-err/20 text-err rounded-md text-[12px] cursor-pointer border border-err/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {batchStartingAction === 'delete' ? t('actions.starting') : t('common.delete_selected')}
            </button>
            <button
              onClick={() => setSelectedAccounts(new Map())}
              disabled={selectedAccounts.size === 0 || batchBusy}
              className="px-3 py-1.5 bg-transparent hover:bg-white/[.05] text-text-muted hover:text-text-primary rounded-md text-[12px] cursor-pointer border border-transparent disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {t('common.clear_selection')}
            </button>
          </div>}
        </div>

        {/* Section Title */}
        <div className="text-[12px] font-semibold text-text-secondary uppercase tracking-wider mb-3.5 flex items-center gap-1.5">
          <span>{t('common.accounts_pool')}</span>
          <span className="font-normal text-text-muted">({filteredTotal})</span>
        </div>

        {/* Grid */}
        {filteredTotal === 0 ? (
          <div className="text-center py-16 text-text-secondary text-sm">
            {t('common.no_matching_accounts')}
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(380px,1fr))] items-start gap-2.5 mb-4 max-sm:grid-cols-1">
            {pagedGroups.map(group => (
              <AccountGroupCard
                key={group.id}
                group={group}
                selectedAccounts={selectedAccounts}
                onToggleWorkspace={toggleSelectedAccount}
                onChanged={loadData}
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
              ‹ {t('common.prev_page')}
            </button>
            <span className="text-[12px] text-text-secondary tabular-nums px-3">
              {page + 1} / {totalPages}
            </span>
            <button
              onClick={() => setPage(p => Math.min(totalPages - 1, p + 1))}
              disabled={page >= totalPages - 1}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[12px] cursor-pointer transition-colors border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              {t('common.next_page')} ›
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
      {showAddModal && (
        <AddAccountModal
          onClose={() => setShowAddModal(false)}
          onSuccess={loadData}
        />
      )}
      {showCleanupModal && summary && (
        <CleanupModal
          counts={{
            auth_invalid: summary.authInvalid,
            exhausted: data?.summary?.exhausted_trials ?? 0,
            no_workspace: summary.noWorkspace,
            personal_missing: data?.summary?.personal_instructions_missing ?? 0,
          }}
          busy={batchBusy}
          onClose={() => setShowCleanupModal(false)}
          onRun={handleCleanupTarget}
        />
      )}

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
