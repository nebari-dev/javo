import type { LucideIcon } from 'lucide-react'

export interface IconOption<T extends string> {
  value: T
  label: string
  icon: LucideIcon
}

/** A segmented control of icon buttons; the chosen one is pressed. */
export function IconSwitch<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string
  options: IconOption<T>[]
  value: T
  onChange: (value: T) => void
}) {
  return (
    <div className="seg icons" role="group" aria-label={label}>
      {options.map(({ value: v, label: l, icon: Icon }) => (
        <button key={v} type="button" aria-label={l} title={l} aria-pressed={value === v} className={value === v ? 'on' : ''} onClick={() => onChange(v)}>
          <Icon size={15} aria-hidden="true" />
        </button>
      ))}
    </div>
  )
}
