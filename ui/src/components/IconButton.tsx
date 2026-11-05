import type { LucideIcon } from 'lucide-react'
import { useState, type ButtonHTMLAttributes, type Ref } from 'react'

interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  icon: LucideIcon
  /** What the button does; its accessible name and its tooltip. */
  label: string
  /** For a toggle: whether it is on. */
  pressed?: boolean
  ref?: Ref<HTMLButtonElement>
}

/**
 * A compact key with only an icon, named by its tooltip. The tooltip is drawn
 * from the label (not extra text in the page), shows on hover and keyboard
 * focus, and Escape hides it (WCAG 1.4.13).
 */
export function IconButton({ icon: Icon, label, pressed, className, onKeyDown, onBlur, onMouseLeave, ...rest }: IconButtonProps) {
  const [hidden, setHidden] = useState(false)
  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={pressed}
      data-tip={label}
      className={['icon-btn', hidden ? 'tip-off' : '', className ?? ''].filter(Boolean).join(' ')}
      onKeyDown={(e) => {
        if (e.key === 'Escape') setHidden(true)
        onKeyDown?.(e)
      }}
      onBlur={(e) => {
        setHidden(false)
        onBlur?.(e)
      }}
      onMouseLeave={(e) => {
        setHidden(false)
        onMouseLeave?.(e)
      }}
      {...rest}
    >
      <Icon size={16} aria-hidden="true" />
    </button>
  )
}
