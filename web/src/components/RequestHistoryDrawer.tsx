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

interface Props {
  open: boolean
  onClose: () => void
}

const PAGE_SIZE = 50

export function RequestHistoryDrawer({ open, onClose }: Props) {
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
      setError(e?.message || '加载调用记录失败')
    } finally {
      if (!silent) setLoading(false)
    }
  }, [page, debouncedQuery, status, api, promptMode])

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
    if (!window.confirm('确认清空全部调用记录？只会删除诊断记录，不会影响账号、配置或 Token 统计。')) return
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
      setError(e?.message || '清空失败')
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
            <span className="text-[14px] font-semibold tracking-tight">调用记录</span>
            {data && (
              <span className="text-[11px] text-text-muted font-normal">
                ({data.filtered_total}{data.filtered_total !== data.total ? ` / 共 ${data.total}` : ''})
              </span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <button
              type="button"
              onClick={() => reload()}
              disabled={loading}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-50"
              title="刷新"
            >
              <IconRotate size={14} className={loading ? 'animate-spin' : ''} />
            </button>
            <button
              type="button"
              onClick={handleClear}
              disabled={clearing || (data?.total ?? 0) === 0}
              className="text-text-secondary hover:text-err bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-30 disabled:cursor-not-allowed"
              title="清空调用记录"
            >
              {clearing ? <IconSpinner size={14} className="animate-spin" /> : <IconTrash size={14} />}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center"
              title="关闭"
            >
              <IconClose size={16} />
            </button>
          </div>
        </div>

        <div className="px-5 py-3 border-b border-border bg-[#171717] max-sm:px-3">
          <div className="text-[11px] text-text-secondary mb-3 leading-relaxed">
            这里只保存模型、账号、提示词模式、Tools 数量、Token、耗时和错误原因。
            <span className="text-ok ml-1">不会保存问题、回答、System Prompt、工具参数或官网个人指令正文。</span>
          </div>
          <div className="grid grid-cols-[minmax(220px,1fr)_150px_170px_210px] gap-2 max-lg:grid-cols-2 max-sm:grid-cols-1">
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索模型、Notion 内部模型或账号…"
              className="w-full px-3 py-2 bg-bg-input border border-border rounded-md text-[12px] text-text-primary outline-none focus:border-white/20 placeholder:text-text-muted"
            />
            <FilterSelect value={status} onChange={setStatus}>
              <option value="all">全部状态</option>
              <option value="success">成功</option>
              <option value="error">失败</option>
            </FilterSelect>
            <FilterSelect value={api} onChange={setAPI}>
              <option value="all">全部接口</option>
              <option value="anthropic">Anthropic</option>
              <option value="openai_chat">OpenAI Chat</option>
              <option value="openai_responses">OpenAI Responses</option>
            </FilterSelect>
            <FilterSelect value={promptMode} onChange={setPromptMode}>
              <option value="all">全部提示词模式</option>
              <option value="existing_prompt">客户端 System Prompt</option>
              <option value="notion_personal_instructions">官网个人指令</option>
              <option value="client_and_notion_personal">客户端 + 官网个人指令</option>
              <option value="no_behavior_prompt">两者都关</option>
              <option value="not_applicable">不适用</option>
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
              {data?.total ? '没有匹配的调用记录' : '尚无调用记录，发起一次 API 请求后会显示在这里'}
            </div>
          )}
          {entries.length > 0 && (
            <div className="min-w-[1040px]">
              <div className="sticky top-0 z-10 grid grid-cols-[150px_88px_110px_minmax(220px,1.35fr)_minmax(170px,1fr)_150px_70px_105px_80px] gap-3 px-5 py-2 bg-[#1d1d1d] border-b border-border text-[10px] uppercase tracking-wider text-text-muted">
                <span>时间</span>
                <span>状态</span>
                <span>接口</span>
                <span>客户端模型 → Notion 模型</span>
                <span>使用账号</span>
                <span>提示词模式</span>
                <span>Tools</span>
                <span>Token</span>
                <span>耗时</span>
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
            最多保留最近 100 条 · 服务重启后仍保留
          </span>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setPage((current) => Math.max(0, current - 1))}
              disabled={page === 0}
              className="px-2.5 py-1.5 bg-bg-card hover:bg-bg-card-hover text-text-secondary rounded-md text-[11px] cursor-pointer border border-border disabled:opacity-30 disabled:cursor-not-allowed"
            >
              上一页
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
              下一页
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
  const created = new Date(entry.created_at).toLocaleString('zh-CN', { hour12: false })
  const success = entry.status === 'success'
  const requestedModel = entry.requested_model || '未识别'
  const notionModel = entry.notion_model || '未发送'

  return (
    <div className={`px-5 py-3 ${success ? 'hover:bg-white/[.015]' : 'bg-err/[.025] hover:bg-err/[.045]'}`}>
      <div className="grid grid-cols-[150px_88px_110px_minmax(220px,1.35fr)_minmax(170px,1fr)_150px_70px_105px_80px] gap-3 items-start">
        <div className="text-[11px] text-text-secondary tabular-nums leading-5">{created}</div>
        <div>
          <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] ${success ? 'text-ok bg-ok/10' : 'text-err bg-err/10'}`}>
            <span className={`w-1.5 h-1.5 rounded-full ${success ? 'bg-ok' : 'bg-err'}`} />
            {success ? '成功' : '失败'}
          </span>
          {!success && entry.http_status > 0 && (
            <div className="text-[10px] text-text-muted mt-1">HTTP {entry.http_status}</div>
          )}
        </div>
        <div className="text-[11px] text-text-secondary leading-5">{formatAPI(entry.api)}</div>
        <div className="min-w-0">
          <div className="text-[11px] text-text-primary font-mono break-all leading-5">
            {requestedModel}
            {entry.used_default_model && <span className="text-text-muted font-sans ml-1">（默认）</span>}
          </div>
          <div className="text-[10px] text-text-muted font-mono break-all leading-5">→ {notionModel}</div>
        </div>
        <div className="text-[11px] text-text-secondary font-mono break-all leading-5">
          {entry.account_email || '未选择'}
          {entry.attempts > 1 && (
            <div className="text-[10px] text-warn font-sans">共尝试 {entry.attempts} 次</div>
          )}
        </div>
        <div className="text-[11px] text-text-secondary leading-5">{formatPromptMode(entry.prompt_mode)}</div>
        <div className={`text-[11px] tabular-nums leading-5 ${entry.tool_count > 0 ? 'text-notion-blue' : 'text-text-muted'}`}>
          {entry.tool_count > 0 ? `${entry.tool_count} 个` : '无'}
        </div>
        <div className="text-[10px] text-text-secondary tabular-nums leading-5">
          <div>入 {formatCompactNumber(entry.input_tokens)}</div>
          <div>出 {formatCompactNumber(entry.output_tokens)}</div>
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
              title="复制错误原因"
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
      return '官网个人指令'
    case 'client_and_notion_personal':
      return '客户端 + 官网个人指令'
    case 'no_behavior_prompt':
      return '两者都关'
    case 'not_applicable':
      return '不适用'
    case 'existing_prompt':
      return '客户端 System Prompt'
    default:
      return mode || '—'
  }
}

function formatDuration(durationMs: number): string {
  if (durationMs < 1000) return `${Math.max(0, durationMs)} ms`
  if (durationMs < 60_000) return `${(durationMs / 1000).toFixed(durationMs < 10_000 ? 1 : 0)} 秒`
  const minutes = Math.floor(durationMs / 60_000)
  const seconds = Math.round((durationMs % 60_000) / 1000)
  return `${minutes}分 ${seconds}秒`
}

function formatCompactNumber(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1000) return `${(value / 1000).toFixed(1)}K`
  return String(Math.max(0, value || 0))
}
