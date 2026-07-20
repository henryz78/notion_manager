import { useCallback, useEffect, useState } from 'react'
import type { RegisterJob, RegisterStep } from '../types'
import { deleteRegisterJob, getJob, listJobs, retryRegisterJob } from '../api'
import { providerDisplay } from '../utils'
import { useT } from '../i18n'
import {
  IconCheck,
  IconChevronRight,
  IconClose,
  IconHistory,
  IconRotate,
  IconSpinner,
  IconTrash,
  IconX,
} from './Icons'

interface Props {
  open: boolean
  onClose: () => void
  onRetryStarted?: (jobId: string) => void
}

export function HistoryDrawer({ open, onClose, onRetryStarted }: Props) {
  const { t, locale } = useT()
  const [jobs, setJobs] = useState<RegisterJob[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [details, setDetails] = useState<Record<string, RegisterJob | undefined>>({})
  const [pendingAction, setPendingAction] = useState<Record<string, 'retry' | 'delete' | undefined>>({})
  const [rowError, setRowError] = useState<Record<string, string | undefined>>({})

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await listJobs(50)
      setJobs(list)
    } catch (e: any) {
      setError(e?.message ?? t('historyDrawer.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    if (!open) return
    reload()
  }, [open, reload])

  useEffect(() => {
    if (!open) return
    const hasRunning = jobs.some((j) => j.state === 'running')
    if (!hasRunning) return
    const tTimer = setInterval(() => { reload() }, 2000)
    return () => clearInterval(tTimer)
  }, [open, jobs, reload])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const toggleExpand = async (id: string) => {
    if (expandedId === id) {
      setExpandedId(null)
      return
    }
    setExpandedId(id)
    if (!details[id]) {
      try {
        const detail = await getJob(id)
        setDetails((prev) => ({ ...prev, [id]: detail }))
      } catch (e) {
        // ignore
      }
    }
  }

  const handleRetry = useCallback(async (id: string) => {
    setRowError((p) => ({ ...p, [id]: undefined }))
    setPendingAction((p) => ({ ...p, [id]: 'retry' }))
    try {
      const resp = await retryRegisterJob(id)
      onRetryStarted?.(resp.job_id)
      reload()
    } catch (e: any) {
      setRowError((p) => ({ ...p, [id]: e?.message ?? t('historyDrawer.retry') }))
    } finally {
      setPendingAction((p) => ({ ...p, [id]: undefined }))
    }
  }, [reload, onRetryStarted, t])

  const handleDelete = useCallback(async (id: string) => {
    if (!window.confirm('Delete this registration task?')) {
      return
    }
    setRowError((p) => ({ ...p, [id]: undefined }))
    setPendingAction((p) => ({ ...p, [id]: 'delete' }))
    try {
      await deleteRegisterJob(id)
      setJobs((prev) => prev.filter((j) => j.id !== id))
      setDetails((prev) => {
        const next = { ...prev }
        delete next[id]
        return next
      })
      if (expandedId === id) setExpandedId(null)
    } catch (e: any) {
      setRowError((p) => ({ ...p, [id]: e?.message ?? t('historyDrawer.delete') }))
    } finally {
      setPendingAction((p) => ({ ...p, [id]: undefined }))
    }
  }, [expandedId, t])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-[90] flex justify-end" onClick={onClose}>
      <div className="absolute inset-0 bg-black/50 backdrop-blur-sm" />
      <div
        className="relative w-full max-w-[640px] h-full bg-bg-secondary border-l border-border shadow-2xl flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-border max-sm:px-3">
          <div className="flex items-center gap-2 text-text-primary">
            <IconHistory size={16} />
            <span className="text-[14px] font-semibold tracking-tight">{t('historyDrawer.title')}</span>
            {!loading && (
              <span className="text-[11px] text-text-muted font-normal">({jobs.length})</span>
            )}
          </div>
          <div className="flex items-center gap-1">
            <button
              onClick={reload}
              disabled={loading}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-50"
              title={t('actions.refreshData')}
            >
              <IconRotate size={14} className={loading ? 'animate-spin' : ''} />
            </button>
            <button
              onClick={onClose}
              className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center"
              title={t('common.close')}
            >
              <IconClose size={16} />
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-auto">
          {error && (
            <div className="m-4 px-3 py-2 bg-err/10 border border-err/30 rounded-md text-[12px] text-err">{error}</div>
          )}
          {!loading && !error && jobs.length === 0 && (
            <div className="text-center py-16 text-text-secondary text-[13px]">{t('historyDrawer.empty')}</div>
          )}
          {jobs.length > 0 && (
            <div className="divide-y divide-white/[.05]">
              {jobs.map((job) => (
                <JobRow
                  key={job.id}
                  job={job}
                  expanded={expandedId === job.id}
                  detail={details[job.id]}
                  pendingAction={pendingAction[job.id]}
                  rowError={rowError[job.id]}
                  onToggle={() => toggleExpand(job.id)}
                  onRetry={() => handleRetry(job.id)}
                  onDelete={() => handleDelete(job.id)}
                  locale={locale}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function JobRow({
  job,
  expanded,
  detail,
  pendingAction,
  rowError,
  onToggle,
  onRetry,
  onDelete,
  locale,
}: {
  job: RegisterJob
  expanded: boolean
  detail: RegisterJob | undefined
  pendingAction: 'retry' | 'delete' | undefined
  rowError: string | undefined
  onToggle: () => void
  onRetry: () => void
  onDelete: () => void
  locale: string
}) {
  const { t } = useT()
  const created = new Date(job.created_at).toLocaleString(locale, { hour12: false })
  const stateLabel =
    job.state === 'running' ? t('common.processing') : job.state === 'cancelled' ? t('common.closed') : t('registerModal.stepSuccess')
  const stateClass =
    job.state === 'running'
      ? 'text-notion-blue bg-notion-blue/10'
      : job.fail > 0
      ? 'text-warn bg-warn/10'
      : 'text-ok bg-ok/10'

  const isRunning = job.state === 'running'
  const canRetry = !isRunning && job.fail > 0 && pendingAction == null
  const canDelete = !isRunning && pendingAction == null
  const proxyShort = job.proxy ? maskProxy(job.proxy) : ''

  return (
    <div className="px-4 py-3">
      <div className="flex items-start gap-2">
        <button
          onClick={onToggle}
          className="flex-1 min-w-0 flex items-center gap-3 text-left bg-transparent border-none p-0 cursor-pointer"
        >
          <span className={`text-text-secondary transition-transform ${expanded ? 'rotate-90' : ''}`}>
            <IconChevronRight size={14} />
          </span>
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="text-[12px] text-text-primary tabular-nums">{created}</span>
              <span className={`text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded ${stateClass}`}>
                {stateLabel}
              </span>
              {job.provider && (
                <span className="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-white/[.06] text-text-secondary">
                  {providerDisplay(job.provider)}
                </span>
              )}
              <span className="text-[11px] text-text-secondary tabular-nums">
                OK <span className="text-ok">{job.ok}</span> / {t('common.failed')} <span className="text-err">{job.fail}</span> / {job.total}
              </span>
              <span className="text-[11px] text-text-muted">{t('registerModal.concurrencyLabel')} {job.concurrency}</span>
              {proxyShort && (
                <span
                  className="text-[10px] px-1.5 py-0.5 rounded bg-white/[.04] text-text-muted font-mono"
                  title={`Proxy: ${job.proxy}`}
                >
                  via {proxyShort}
                </span>
              )}
            </div>
          </div>
        </button>
        <div className="flex items-center gap-1 shrink-0">
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); onRetry() }}
            disabled={!canRetry}
            title={t('historyDrawer.retry')}
            className="text-text-secondary hover:text-text-primary bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-30 disabled:cursor-not-allowed"
          >
            {pendingAction === 'retry' ? (
              <IconSpinner size={13} className="animate-spin" />
            ) : (
              <IconRotate size={13} />
            )}
          </button>
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); onDelete() }}
            disabled={!canDelete}
            title={t('historyDrawer.delete')}
            className="text-text-secondary hover:text-err bg-transparent border-none cursor-pointer p-1 flex items-center disabled:opacity-30 disabled:cursor-not-allowed"
          >
            {pendingAction === 'delete' ? (
              <IconSpinner size={13} className="animate-spin" />
            ) : (
              <IconTrash size={13} />
            )}
          </button>
        </div>
      </div>
      {rowError && (
        <div className="ml-6 mt-1 px-2 py-1 text-[11px] text-err bg-err/10 border border-err/30 rounded">
          {rowError}
        </div>
      )}
      {expanded && (
        <div className="mt-2 ml-6 border border-border rounded-md divide-y divide-white/[.05] max-h-[420px] overflow-auto">
          {(detail || job).steps.map((s, i) => (
            <DetailStep key={`${s.email}-${i}`} step={s} index={i} />
          ))}
          {!detail && (
            <div className="px-3 py-2 text-[11px] text-text-muted flex items-center gap-1">
              <IconSpinner size={11} className="animate-spin" /> {t('historyDrawer.loading')}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function maskProxy(raw: string): string {
  try {
    const u = new URL(raw)
    return `${u.hostname}${u.port ? ':' + u.port : ''}`
  } catch {
    return raw.length > 32 ? raw.slice(0, 32) + '…' : raw
  }
}

function DetailStep({ step, index }: { step: RegisterStep; index: number }) {
  const { t } = useT()
  const [open, setOpen] = useState(false)
  let icon: React.ReactNode
  let label: string
  let cls = ''
  switch (step.status) {
    case 'ok':
      icon = <IconCheck size={11} />
      label = t('common.success')
      cls = 'text-ok'
      break
    case 'fail':
      icon = <IconX size={11} />
      label = t('common.failed')
      cls = 'text-err'
      break
    case 'running':
      icon = <IconSpinner size={11} className="animate-spin" />
      label = t('common.processing')
      cls = 'text-notion-blue'
      break
    default:
      icon = null
      label = t('common.loading')
      cls = 'text-text-muted'
  }
  return (
    <div className={`px-3 py-2 ${step.status === 'fail' ? 'bg-err/[.04]' : ''}`}>
      <div className="flex items-center gap-2">
        <span className="text-[10px] text-text-muted w-7 tabular-nums shrink-0">#{index + 1}</span>
        <span className="text-[12px] text-text-primary font-mono truncate flex-1 min-w-0">{step.email || '—'}</span>
        <span className={`inline-flex items-center gap-1 text-[11px] ${cls} shrink-0`}>
          {icon}
          {label}
        </span>
      </div>
      {step.status === 'fail' && step.message && (
        <div className="mt-1 ml-9">
          <button
            onClick={() => setOpen((v) => !v)}
            className="text-[11px] text-text-secondary hover:text-text-primary inline-flex items-center gap-1 bg-transparent border-none p-0 cursor-pointer"
          >
            <IconChevronRight size={11} className={open ? 'rotate-90 transition-transform' : 'transition-transform'} />
            {open ? t('common.close') : t('common.loading')}
          </button>
          {open && (
            <pre className="mt-1 p-2 bg-bg-input border border-border rounded text-[11px] text-text-secondary whitespace-pre-wrap break-all max-h-48 overflow-auto font-mono">
              {step.message}
            </pre>
          )}
        </div>
      )}
      {step.status === 'ok' && step.file && (
        <div className="mt-0.5 ml-9 text-[10px] text-text-muted truncate">→ {step.file}</div>
      )}
    </div>
  )
}
