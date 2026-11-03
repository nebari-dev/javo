import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { PartIcon, type Part } from './Section'

interface TabItem {
  id: string
  label: string
  /** The tab's icon, before its name; hidden from screen readers. */
  icon?: Part
  panel: ReactNode
}

/** WAI-ARIA tabs: arrow keys, Home and End move and select; only the selected tab is in the tab order. */
export function Tabs({ label, tabs }: { label: string; tabs: TabItem[] }) {
  const [selected, setSelected] = useState(tabs[0]?.id)
  const base = useId()
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})
  const current = tabs.find((t) => t.id === selected) ?? tabs[0]

  const go = (i: number) => {
    const t = tabs[(i + tabs.length) % tabs.length]
    setSelected(t.id)
    refs.current[t.id]?.focus()
  }
  const onKeyDown = (e: KeyboardEvent, i: number) => {
    const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key]
    if (to === undefined) return
    e.preventDefault()
    go(to)
  }

  return (
    <div className="tabs">
      <div role="tablist" aria-label={label} className="tablist">
        {tabs.map((t, i) => (
          <button
            key={t.id}
            ref={(el) => {
              refs.current[t.id] = el
            }}
            type="button"
            role="tab"
            id={`${base}-${t.id}-tab`}
            aria-selected={t.id === current.id}
            aria-controls={`${base}-${t.id}-panel`}
            tabIndex={t.id === current.id ? 0 : -1}
            className={t.id === current.id ? 'on' : ''}
            onClick={() => setSelected(t.id)}
            onKeyDown={(e) => onKeyDown(e, i)}
          >
            {t.icon && <PartIcon part={t.icon} size={16} />}
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${base}-${current.id}-panel`} aria-labelledby={`${base}-${current.id}-tab`} tabIndex={0}>
        {current.panel}
      </div>
    </div>
  )
}
