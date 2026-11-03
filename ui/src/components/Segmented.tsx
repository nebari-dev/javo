import type { LucideIcon } from 'lucide-react'
import { useRef, type KeyboardEvent } from 'react'

interface SegmentedOption<T extends string> {
  value: T
  label: string
  /** A line icon drawn before the label. */
  icon?: LucideIcon
  disabled?: boolean
}

interface SegmentedProps<T extends string> {
  label: string
  /** The chosen value; '' when none is chosen. */
  value: T | ''
  options: SegmentedOption<T>[]
  onChange: (value: T) => void
}

/** A WAI-ARIA radio group drawn as keys: arrow keys only move focus (so a stray key never commits a choice); Space, Enter or click chooses, skipping disabled options; one tab stop. */
export function Segmented<T extends string>({ label, value, options, onChange }: SegmentedProps<T>) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})
  const enabled = options.filter((o) => !o.disabled)
  const stop = enabled.find((o) => o.value === value) ?? enabled[0]

  const onKeyDown = (e: KeyboardEvent, from: T) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key]
    if (step === undefined || enabled.length === 0) return
    e.preventDefault()
    const i = enabled.findIndex((o) => o.value === from)
    const next = enabled[(i + step + enabled.length) % enabled.length]
    refs.current[next.value]?.focus()
  }

  return (
    <div role="radiogroup" aria-label={label} className="seg">
      {options.map((o) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[o.value] = el
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          aria-disabled={o.disabled || undefined}
          tabIndex={o === stop ? 0 : -1}
          className={[o.value === value ? 'on' : '', o.icon ? 'with-icon' : ''].filter(Boolean).join(' ') || undefined}
          onClick={() => !o.disabled && onChange(o.value)}
          onKeyDown={(e) => onKeyDown(e, o.value)}
        >
          {o.icon && <o.icon size={16} aria-hidden="true" />}
          {o.label}
        </button>
      ))}
    </div>
  )
}
