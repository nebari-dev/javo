import { useEffect, useRef, useState, type ReactNode, type Ref } from 'react'

interface ChosenRowProps {
  /** The logo tile on the left. */
  icon: ReactNode
  title: ReactNode
  /** The muted line under the title, if any. */
  line?: string
  /** The button's accessible name; ", change" is added. */
  label: string
  buttonRef?: Ref<HTMLButtonElement>
  /** Fades the row in; set once the person folds or unfolds something, never on the first paint. */
  animate?: boolean
  describedBy?: string
  onChange: () => void
}

/** A choice already made, as one row; pressing it opens the full choice again. */
export function ChosenRow({ icon, title, line, label, buttonRef, animate, describedBy, onChange }: ChosenRowProps) {
  return (
    <button
      ref={buttonRef}
      type="button"
      className={`card choice tpl chosen${animate ? ' tpl-in' : ''}`}
      aria-label={`${label}, change`}
      aria-describedby={describedBy}
      onClick={onChange}
    >
      {icon}
      <span>
        <span className="chosen-name">{title}</span>
        {line && <span className="muted small block one-line">{line}</span>}
      </span>
      <span className="btn sm" aria-hidden="true">
        Change
      </span>
    </button>
  )
}

interface FoldedProps {
  /** Whether the card holds a sensible choice, so it may show as one row. */
  foldable: boolean
  /** Something inside the person must see, such as an error on a field: the card opens and stays open. */
  attention: boolean
  icon: ReactNode
  /** What the card is about: "Code", "Size". */
  title: string
  /** The choice it holds: "Small · 1 CPU · 2 GB". */
  value: string
  /** The full card. */
  children: ReactNode
}

/**
 * A card that holds a sensible default shows as one row ("Size: Small ·
 * 1 CPU · 2 GB, change"); Change opens the full card with focus on its
 * current choice. Something inside that needs attention, such as an
 * error, opens it, and once shown open it stays open.
 */
export function Folded({ foldable, attention, icon, title, value, children }: FoldedProps) {
  const [opened, setOpened] = useState(false)
  // Set by Change: the opened card fades in and takes focus; a card open from the start does neither.
  const [byHand, setByHand] = useState(false)
  const body = useRef<HTMLDivElement>(null)
  // Open once, open for good: it never folds under the person, even when it later holds a sensible choice.
  useEffect(() => {
    if (attention || !foldable) setOpened(true)
  }, [attention, foldable])
  const open = opened || !foldable
  useEffect(() => {
    if (!open || !byHand) return
    const el = body.current
    const target =
      el?.querySelector<HTMLElement>('input:checked') ??
      el?.querySelector<HTMLElement>('input, button, select, textarea, [tabindex]:not([tabindex="-1"])')
    target?.focus()
  }, [open, byHand])
  if (!open) {
    return (
      <ChosenRow
        icon={icon}
        title={value}
        label={`${title}: ${value}`}
        onChange={() => {
          setByHand(true)
          setOpened(true)
        }}
      />
    )
  }
  return (
    <div ref={body} className={byHand ? 'tpl-in' : undefined}>
      {children}
    </div>
  )
}
