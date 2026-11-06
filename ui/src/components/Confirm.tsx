import type { LucideIcon } from 'lucide-react'
import { useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { problemText } from '../lib/http'
import { Button } from './Button'

/** A confirmation over the page: Cancel is focused first; Escape or a click outside cancels. */
export function Confirm(props: {
  title: ReactNode
  body: string
  confirm: string
  /** The cancel button's label; default Cancel. */
  cancel?: string
  icon: LucideIcon
  danger?: boolean
  busy: boolean
  error?: unknown
  onConfirm: () => void
  onCancel: () => void
}) {
  const ids = { title: useId(), body: useId() }
  const cancel = useRef<HTMLButtonElement>(null)
  // Escape cancels wherever focus is, and stops there so a panel underneath
  // (the command palette) does not close too.
  const onCancel = useRef(props.onCancel)
  onCancel.current = props.onCancel
  useEffect(() => {
    cancel.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.code !== 'Escape') return
      e.stopPropagation()
      onCancel.current()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])
  return createPortal(
    // A modal panel underneath (the command palette) turns pointer events off
    // on the body; this dialog is above it and takes clicks again.
    <div
      className="overlay open confirm-overlay"
      style={{ pointerEvents: 'auto' }}
      data-testid="confirm-backdrop"
      onMouseDown={(e) => e.target === e.currentTarget && props.onCancel()}
    >
      <div
        className="confirm"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={ids.title}
        aria-describedby={ids.body}
      >
        <b id={ids.title}>{props.title}</b>
        <p id={ids.body} className="tight muted small">
          {props.body}
        </p>
        <div className="row">
          <Button ref={cancel} size="sm" onClick={props.onCancel}>
            {props.cancel ?? 'Cancel'}
          </Button>
          <Button size="sm" variant={props.danger ? 'danger' : 'primary'} icon={props.icon} disabled={props.busy} onClick={props.onConfirm}>
            {props.confirm}
          </Button>
        </div>
        {props.error ? <span className="errmsg">{problemText(props.error)}</span> : null}
      </div>
    </div>,
    document.body,
  )
}

