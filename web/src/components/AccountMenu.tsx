import { useEffect, useRef, useState } from 'react'
import type { AccountInfo } from '../types'
import { bulkAccountAction, deleteAccount, openProxy } from '../api'
import { IconCopy, IconExternalLink, IconMore, IconPlay, IconTrash, IconX } from './Icons'
import { useT } from '../i18n'

interface Props {
  account: AccountInfo
  onChanged: () => void
}

// AccountMenu renders the per-card 3-dot dropdown. Stop propagation on the
// trigger so clicking it doesn't bubble up to the card's "open proxy"
// click handler.
export function AccountMenu({ account, onChanged }: Props) {
  const { t } = useT()
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [toggling, setToggling] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDocClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false)
        setConfirming(false)
      }
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        setConfirming(false)
      }
    }
    window.addEventListener('mousedown', onDocClick)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', onDocClick)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const onCopyToken = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!account.token_v2) return
    try {
      await navigator.clipboard.writeText(account.token_v2)
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    } catch {
      // best effort
    }
  }

  const onOpenProxy = (e: React.MouseEvent) => {
    e.stopPropagation()
    openProxy(account.email)
    setOpen(false)
  }

  const onDelete = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!confirming) {
      setConfirming(true)
      return
    }
    setDeleting(true)
    try {
      await deleteAccount(account.email)
      onChanged()
    } catch (err) {
      console.error('delete account failed', err)
    } finally {
      setDeleting(false)
      setOpen(false)
      setConfirming(false)
    }
  }

  const onToggleDisabled = async (e: React.MouseEvent) => {
    e.stopPropagation()
    setToggling(true)
    try {
      await bulkAccountAction(account.disabled ? 'enable' : 'disable', [account.email])
      onChanged()
    } catch (err) {
      console.error('toggle account disabled state failed', err)
    } finally {
      setToggling(false)
      setOpen(false)
    }
  }

  return (
    <div ref={wrapRef} className="relative" onClick={(e) => e.stopPropagation()}>
      <button
        onClick={(e) => {
          e.stopPropagation()
          setOpen((v) => !v)
        }}
        className="w-6 h-6 rounded hover:bg-white/[.08] text-text-secondary hover:text-text-primary flex items-center justify-center bg-transparent border-none cursor-pointer transition-colors"
        title={t('accountMenu.moreActions')}
      >
        <IconMore size={14} />
      </button>
      {open && (
        <div
          className="absolute right-0 top-7 z-30 w-44 bg-bg-secondary border border-border rounded-md shadow-xl shadow-black/40 py-1"
          onClick={(e) => e.stopPropagation()}
        >
          <MenuItem
            onClick={onCopyToken}
            disabled={!account.token_v2}
            icon={<IconCopy size={13} />}
            label={copied ? t('accountMenu.copiedToken') : t('accountMenu.copyToken')}
          />
          <MenuItem onClick={onOpenProxy} disabled={account.disabled} icon={<IconExternalLink size={13} />} label={t('accountMenu.openProxy')} />
          <MenuItem
            onClick={onToggleDisabled}
            disabled={toggling}
            icon={account.disabled ? <IconPlay size={13} /> : <IconX size={13} />}
            label={toggling ? t('accountMenu.processing') : account.disabled ? t('accountMenu.enableAccount') : t('accountMenu.disableAccount')}
          />
          <div className="border-t border-border my-1" />
          <MenuItem
            onClick={onDelete}
            danger
            disabled={deleting}
            icon={<IconTrash size={13} />}
            label={confirming ? t('accountMenu.confirmDelete') : t('accountMenu.deleteAccount')}
          />
        </div>
      )}
    </div>
  )
}

function MenuItem({
  onClick,
  icon,
  label,
  disabled,
  danger,
}: {
  onClick: (e: React.MouseEvent) => void
  icon: React.ReactNode
  label: string
  disabled?: boolean
  danger?: boolean
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={`w-full flex items-center gap-2 px-3 py-1.5 text-[12px] text-left bg-transparent border-none cursor-pointer transition-colors ${
        danger
          ? 'text-err hover:bg-err/10'
          : 'text-text-secondary hover:text-text-primary hover:bg-white/[.05]'
      } disabled:opacity-40 disabled:cursor-not-allowed`}
    >
      <span className="shrink-0">{icon}</span>
      <span>{label}</span>
    </button>
  )
}
