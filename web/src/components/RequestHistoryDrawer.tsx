import { useCallback, useEffect, useMemo, useState } from 'react'
import type { RequestHistoryEntry, RequestHistoryPage } from '../types'
import { clearRequestHistory, fetchRequestHistory } from '../api'
import {
  IconActivity,
  IconClose,
  IconCopy,
  IconRotate,
  IconSpinner,
  IconTrash,
} from './Icons'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'

interface Props {
  open: boolean
  onClose: () => void
}

const PAGE_SIZE = 50

export function RequestHistoryDrawer({ open, onClose }: Props) {
  const { t } = useTranslation()
  const [data, setData] = useState<RequestHistoryPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [debouncedQuery, setDebouncedQuery] = useState('')
  const [status, setStatus] = useState('all')
  const [api, setAPI] = useState('all')
  const [promptMode, setPromptMode] = useState('all')
  const [page, setPage] = useState(0)
  const [copiedID, setCopiedID] = useState<string | null>(null)

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedQuery(query.trim()), 250)
    return () => window.clearTimeout(timer)
  }, [query])

  useEffect(() => {
    setPage(0)
  }, [debouncedQuery, status, api, promptMode])

  const reload = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    setError(null)
    try {
      const result = await fetchRequestHistory({
        page,
        pageSize: PAGE_SIZE,
        query: debouncedQuery,
        status,
        api,
        promptMode,
      })
      setData(result)
    } catch (e: any) {
      setError(e?.message || t('request_history.load_failed'))
    } finally {
      if (!silent) setLoading(false)
    }
  }, [page, debouncedQuery, status, api, promptMode, t])

  useEffect(() => {
    if (!open) return
    reload()
  }, [open, reload])

  // Keep the first page fresh while the drawer is open so a just-finished API
  // request appears without requiring a manual refresh.
  useEffect(() => {
    if (!open || page !== 0) return
    const timer = window.setInterval(() => { reload(true) }, 5000)
    return () => window.clearInterval(timer)
  }, [open, page, reload])

  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const totalPages = useMemo(
    () => Math.max(1, Math.ceil((data?.filtered_total ?? 0) / PAGE_SIZE)),
    [data?.filtered_total],
  )

  useEffect(() => {
    if (page >= totalPages && page > 0) setPage(totalPages - 1)
  }, [page, totalPages])

  const handleClear = async () => {
    if (!window.confirm(t('request_history.confirm_clear'))) return
    setClearing(true)
    setError(null)
    try {
      await clearRequestHistory()
      setPage(0)
      setData({
        total: 0,
        filtered_total: 0,
        page: 0,
        page_size: PAGE_SIZE,
        entries: [],
      })
    } catch (e: any) {
      setError(e?.message || t('request_history.clear_failed'))
    } finally {
      setClearing(false)
    }
  }

  const copyError = async (entry: RequestHistoryEntry) => {
    if (!entry.error) return
    await navigator.clipboard.writeText(entry.error)
    setCopiedID(entry.id)
    window.setTimeout(() => setCopiedID(null), 1200)
  }

  if (!open) return null

  const entries = data?.entries ?? []

  return (
    <div className="fixed inset-0 z-[90] flex justify-end" onClick={onClose}>
      <div className="absolute inset-0 bg-black/55 backdrop-blur-sm" />
      <div
        className="relative w-full max-w-[1180px] h-full bg-bg-secondary border-l border-border shadow-2xl flex flex-col"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between gap-4 px-5 py-3.5 border-b border-border max-sm:px-3">
          <div className="flex items-center gap-2 text-text-primary min-w-0">
            <IconActivity size={16} />
            <span className="text-[14px] font-semibold tracking-tight">{t('request_history.title')}</span>
            {data && (
              <span className="text-[11px] text-text-muted font-normal">
                ({data.filtered_total}{data.filtered_total !== data.total ? ` / ${t('request_history.count_total', { filtered: data.filtered_total, total: data.total }).replace(`${data.filtered_total} / `, '')}` : ''})
              </span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <button
              type="button"
              onClick={() => reload()}
              disabled={loading}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-50"
              title={t('history.refresh')}
            >
              <IconRotate size={14} className={loading ? 'animate-spin' : ''} />
            </button>
            <button
              type="button"
              onClick={handleClear}
              disabled={clearing || (data?.total ?? 0) === 0}
              className="text-text-secondary hover:text-err bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-30 disabled:cursor-not-allowed"
              title={t('request_history.clear')}
            >
              {clearing ? <IconSpinner size={14} className="animate-spin" /> : <IconTrash size={14} />}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center"
              title={t('common.close')}
            >
              <IconClose size={16} />
            </button>
          </div>
        </div>

        <div className="px-5 py-3 border-b border-border bg-[#171717] max-sm:px-3">
          <div className="text-[11px] text-text-secondary mb-3 leading-relaxed">
            {t('request_history.privacy_summary')}
            <span className="text-ok ml-1">{t('request_history.privacy_detail')}</span>
          </div>
          <div className="grid grid-cols-[minmax(220px,1fr)_150px_170px_210px] gap-2 max-lg:grid-cols-2 max-sm:grid-cols-1">
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t('request_history.search')}
              className="w-full px-3 py-2 bg-bg-input border border-border rounded-md text-[12px] text-text-primary outline-none focus:border-white/20 placeholder:text-text-muted"
            />
            <FilterSelect value={status} onChange={setStatus}>
              <option value="all">{t('request_history.all_statuses')}</option>
              <option value="success">{t('request_history.success')}</option>
              <option value="error">{t('request_history.failed')}</option>
            </FilterSelect>
            <FilterSelect value={api} onChange={setAPI}>
              <option value="all">{t('request_history.all_apis')}</option>
              <option value="anthropic">Anthropic</option>
              <option value="openai_chat">OpenAI Chat</option>
              <option value="openai_responses">OpenAI Responses</option>
            </FilterSelect>
            <FilterSelect value={promptMode} onChange={setPromptMode}>
              <option value="all">{t('request_history.all_prompt_modes')}</option>
              <option value="existing_prompt">{t('request_history.client_prompt')}</option>
              <option value="notion_personal_instructions">{t('request_history.notion_personal')}</option>
              <option value="client_and_notion_personal">{t('request_history.both_prompts')}</option>
              <option value="no_behavior_prompt">{t('request_history.no_prompts')}</option>
              <option value="not_applicable">{t('request_history.not_applicable')}</option>
            </FilterSelect>
          </div>
        </div>

        {error && (
          <div className="mx-5 mt-4 px-3 py-2 bg-err/10 border border-err/30 rounded-md text-[12px] text-err">
            {error}
          </div>
        )}

        <div className="flex-1 overflow-auto">
          {!loading && !error && entries.length === 0 && (
            <div className="text-center py-20 text-text-secondary text-[13px]">
              {data?.total ? t('request_history.no_matches') : t('request_history.empty')}
            </div>
          )}
          {entries.length > 0 && (
            <div className="min-w-[1040px]">
              <div className="sticky top-0 z-10 grid grid-cols-[150px_88px_110px_minmax(220px,1.35fr)_minmax(170px,1fr)_150px_70px_105px_80px] gap-3 px-5 py-2 bg-[#1d1d1d] border-b border-border text-[10px] uppercase tracking-wider text-text-muted">
                <span>{t('request_history.time')}</span>
                <span>{t('request_history.status')}</span>
                <span>{t('request_history.api')}</span>
                <span>{t('request_history.model_route')}</span>
                <span>{t('request_history.account')}</span>
                <span>{t('request_history.prompt_mode')}</span>
                <span>Tools</span>
                <span>Token</span>
                <span>{t('request_history.duration')}</span>
              </div>
              <div className="divide-y divide-white/[.05]">
                {entries.map((entry) => (
                  <RequestRow
                    key={entry.id}
                    entry={entry}
                    copied={copiedID === entry.id}
                    onCopyError={() => copyError(entry)}
                  />
                ))}
              </div>
            </div>
          )}
        </div>

        <div className="flex items-center justify-between gap-3 px-5 py-3 border-t border-border">
          <span className="text-[11px] text-text-muted">
            {t('request_history.retention')}
          </span>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setPage((current) => Math.max(0, current - 1))}
              disabled={page === 0}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[11px] cursor-pointer border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              {t('common.prev_page')}
            </button>
            <span className="text-[11px] text-text-secondary tabular-nums">
              {page + 1} / {totalPages}
            </span>
            <button
              type="button"
              onClick={() => setPage((current) => Math.min(totalPages - 1, current + 1))}
              disabled={page >= totalPages - 1}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[11px] cursor-pointer border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              {t('common.next_page')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function FilterSelect({
  value,
  onChange,
  children,
}: {
  value: string
  onChange: (value: string) => void
  children: React.ReactNode
}) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className="w-full px-3 py-2 bg-bg-input border border-border rounded-md text-[12px] text-text-primary outline-none focus:border-white/20 cursor-pointer"
    >
      {children}
    </select>
  )
}

function RequestRow({
  entry,
  copied,
  onCopyError,
}: {
  entry: RequestHistoryEntry
  copied: boolean
  onCopyError: () => void
}) {
  const { t, i18n: i18nInstance } = useTranslation()
  const created = new Date(entry.created_at).toLocaleString(i18nInstance.language === 'zh' ? 'zh-CN' : 'en-US', { hour12: false })
  const success = entry.status === 'success'
  const requestedModel = entry.requested_model || t('request_history.unknown')
  const notionModel = entry.notion_model || t('request_history.not_sent')

  return (
    <div className={`px-5 py-3 ${success ? 'hover:bg-white/[.015]' : 'bg-err/[.025] hover:bg-err/[.045]'}`}>
      <div className="grid grid-cols-[150px_88px_110px_minmax(220px,1.35fr)_minmax(170px,1fr)_150px_70px_105px_80px] gap-3 items-start">
        <div className="text-[11px] text-text-secondary tabular-nums leading-5">{created}</div>
        <div>
          <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] ${success ? 'text-ok bg-ok/10' : 'text-err bg-err/10'}`}>
            <span className={`w-1.5 h-1.5 rounded-full ${success ? 'bg-ok' : 'bg-err'}`} />
            {success ? t('request_history.success') : t('request_history.failed')}
          </span>
          {!success && entry.http_status > 0 && (
            <div className="text-[10px] text-text-muted mt-1">HTTP {entry.http_status}</div>
          )}
        </div>
        <div className="text-[11px] text-text-secondary leading-5">{formatAPI(entry.api)}</div>
        <div className="min-w-0">
          <div className="text-[11px] text-text-primary font-mono break-all leading-5">
            {requestedModel}
            {entry.used_default_model && <span className="text-text-muted font-sans ml-1">({t('request_history.default')})</span>}
          </div>
          <div className="text-[10px] text-text-muted font-mono break-all leading-5">→ {notionModel}</div>
        </div>
        <div className="text-[11px] text-text-secondary font-mono break-all leading-5">
          {entry.account_email || t('request_history.not_selected')}
          {entry.attempts > 1 && (
            <div className="text-[10px] text-warn font-sans">{t('request_history.attempts', { count: entry.attempts })}</div>
          )}
        </div>
        <div className="text-[11px] text-text-secondary leading-5">{formatPromptMode(entry.prompt_mode)}</div>
        <div className={`text-[11px] tabular-nums leading-5 ${entry.tool_count > 0 ? 'text-notion-blue' : 'text-text-muted'}`}>
          {entry.tool_count > 0 ? String(entry.tool_count) : t('request_history.none')}
        </div>
        <div className="text-[10px] text-text-secondary tabular-nums leading-5">
          <div>{t('request_history.input')} {formatCompactNumber(entry.input_tokens)}</div>
          <div>{t('request_history.output')} {formatCompactNumber(entry.output_tokens)}</div>
        </div>
        <div className="text-[11px] text-text-secondary tabular-nums leading-5">{formatDuration(entry.duration_ms)}</div>
      </div>

      {!success && entry.error && (
        <details className="mt-2 ml-[376px] max-w-[690px] group max-sm:ml-0">
          <summary className="text-[11px] text-err/90 hover:text-err cursor-pointer select-none truncate">
            {entry.error}
          </summary>
          <div className="mt-1 flex items-start gap-2">
            <pre className="flex-1 min-w-0 p-2 bg-bg-input border border-err/20 rounded text-[11px] text-text-secondary whitespace-pre-wrap break-all max-h-40 overflow-auto font-mono">
              {entry.error}
            </pre>
            <button
              type="button"
              onClick={onCopyError}
              className={`shrink-0 p-1.5 rounded border cursor-pointer ${copied ? 'text-ok border-ok/30 bg-ok/10' : 'text-text-secondary border-border bg-bg-card hover:text-text-primary'}`}
              title={t('request_history.copy_error')}
            >
              <IconCopy size={12} />
            </button>
          </div>
        </details>
      )}
    </div>
  )
}

function formatAPI(api: string): string {
  switch (api) {
    case 'openai_chat':
      return 'OpenAI Chat'
    case 'openai_responses':
      return 'Responses'
    case 'anthropic':
      return 'Anthropic'
    default:
      return api || '—'
  }
}

function formatPromptMode(mode: string): string {
  switch (mode) {
    case 'notion_personal_instructions':
      return i18n.t('request_history.notion_personal')
    case 'client_and_notion_personal':
      return i18n.t('request_history.both_prompts')
    case 'no_behavior_prompt':
      return i18n.t('request_history.no_prompts')
    case 'not_applicable':
      return i18n.t('request_history.not_applicable')
    case 'existing_prompt':
      return i18n.t('request_history.client_prompt')
    default:
      return mode || '—'
  }
}

function formatDuration(durationMs: number): string {
  if (durationMs < 1000) return `${Math.max(0, durationMs)} ms`
  if (durationMs < 60_000) return i18n.t('request_history.seconds', { value: (durationMs / 1000).toFixed(durationMs < 10_000 ? 1 : 0) })
  const minutes = Math.floor(durationMs / 60_000)
  const seconds = Math.round((durationMs % 60_000) / 1000)
  return i18n.t('request_history.minutes_seconds', { minutes, seconds })
}

function formatCompactNumber(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1000) return `${(value / 1000).toFixed(1)}K`
  return String(Math.max(0, value || 0))
}
