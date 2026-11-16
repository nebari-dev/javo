import { useEffect, useRef, useState, type ReactNode, type Ref } from 'react'

interface ChosenRowProps {
  /** The logo tile on the left. */
  icon: ReactNode
  title: ReactNode
  /** The muted line under the title, if any. */
  line?: string
  /** The button's accessible name; ", change" (or the verb's) is added. */
  label: string
  /** What pressing it does: "Change" by default, "Done" on top of an open card. */
  verb?: string
  /** Set on a row that folds and unfolds a card under it. */
  expanded?: boolean
  buttonRef?: Ref<HTMLButtonElement>
  /** Fades the row in; set once the person folds or unfolds something, never on the first paint. */
  animate?: boolean
  describedBy?: string
  onChange: () => void
}

/** A choice already made, as one row; pressing it opens the full choice again. */
export function ChosenRow({ icon, title, line, label, verb = 'Change', expanded, buttonRef, animate, describedBy, onChange }: ChosenRowProps) {
  return (
    <button
      ref={buttonRef}
      type="button"
      className={`card choice tpl chosen${animate ? ' tpl-in' : ''}`}
      aria-label={`${label}, ${verb.toLowerCase()}`}
      aria-expanded={expanded}
      aria-describedby={describedBy}
      onClick={onChange}
    >
      {icon}
      <span>
        <span className="chosen-name">{title}</span>
        {line && <span className="muted small block one-line">{line}</span>}
      </span>
      <span className="btn sm" aria-hidden="true">
        {verb}
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
  /** What Change on the row does instead of opening the full card, when one step does the common change (opening a file picker). */
  onChange?: () => void
  /** Other changes, under the row, while it is one row. */
  actions?: ReactNode
}

/**
 * A card that holds a sensible default shows as one row ("Size: Small ·
 * 1 CPU · 2 GB, change"). Change unfolds the full card under the row, with
 * focus on its current choice, and the row, now saying Done, folds it back.
 * Something inside that needs attention, such as an error, opens it, and it
 * never folds by itself.
 */
export function Folded({ foldable, attention, icon, title, value, children, onChange, actions }: FoldedProps) {
  const [opened, setOpened] = useState(false)
  // Set by Change: the opened card takes focus; a card open from the start does not.
  const [byHand, setByHand] = useState(false)
  // The card is mounted from its first opening on, so folding it can animate; it
  // unfolds a frame after mounting, so opening animates too.
  const [mounted, setMounted] = useState(false)
  // Set once fully open: only then may the card's focus rings and menus spill out of it.
  const [settled, setSettled] = useState(false)
  const body = useRef<HTMLDivElement>(null)
  // It never folds under the person, even when it later holds a sensible choice; only the row folds it.
  useEffect(() => {
    if (attention || !foldable) {
      setMounted(true)
      setOpened(true)
    }
  }, [attention, foldable])
  const open = opened || !foldable
  useEffect(() => {
    if (!open || !byHand) return
    const el = body.current
    const target =
      el?.querySelector<HTMLElement>('input:checked') ??
      el?.querySelector<HTMLElement>('input, button, select, textarea, [tabindex]:not([tabindex="-1"])')
    target?.focus({ preventScroll: true })
  }, [open, byHand])
  if (!foldable) return <>{children}</>
  const unfold = () => {
    setByHand(true)
    setMounted(true)
    requestAnimationFrame(() => setOpened(true))
  }
  const fold = () => {
    setByHand(false)
    setSettled(false)
    setOpened(false)
  }
  return (
    <div className="stack tight-gap">
      {!attention && (
        <ChosenRow
          icon={icon}
          title={value}
          label={`${title}: ${value}`}
          verb={open ? 'Done' : 'Change'}
          expanded={onChange && !open ? undefined : open}
          onChange={open ? fold : (onChange ?? unfold)}
        />
      )}
      {!open && actions && <div className="chosen-actions">{actions}</div>}
      {mounted && (
        <div
          className={`fold${open ? ' open' : ''}${settled ? ' settled' : ''}`}
          aria-hidden={!open}
          inert={!open}
          onTransitionEnd={(e) => {
            if (e.target === e.currentTarget && open) setSettled(true)
          }}
        >
          <div ref={body} className="fold-in">
            {children}
          </div>
        </div>
      )}
    </div>
  )
}
