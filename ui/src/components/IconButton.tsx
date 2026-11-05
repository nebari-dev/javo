import type { LucideIcon } from 'lucide-react'
import type { ButtonHTMLAttributes, Ref } from 'react'
import { useTooltip } from './Tooltip'

interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  icon: LucideIcon
  /** What the button does; its accessible name and its tooltip. */
  label: string
  /** For a toggle: whether it is on. */
  pressed?: boolean
  ref?: Ref<HTMLButtonElement>
}

/**
 * A compact key with only an icon, named by its tooltip (useTooltip), which
 * is drawn from the label.
 */
export function IconButton({ icon: Icon, label, pressed, className, onKeyDown, onBlur, onFocus, onMouseEnter, onMouseLeave, ...rest }: IconButtonProps) {
  const tip = useTooltip(label)
  return (
    <>
      <button
        type="button"
        aria-label={label}
        aria-pressed={pressed}
        className={['icon-btn', className ?? ''].filter(Boolean).join(' ')}
        onKeyDown={(e) => {
          tip.handlers.onKeyDown(e)
          onKeyDown?.(e)
        }}
        onFocus={(e) => {
          tip.handlers.onFocus(e)
          onFocus?.(e)
        }}
        onBlur={(e) => {
          tip.handlers.onBlur()
          onBlur?.(e)
        }}
        onMouseEnter={(e) => {
          tip.handlers.onMouseEnter(e)
          onMouseEnter?.(e)
        }}
        onMouseLeave={(e) => {
          tip.handlers.onMouseLeave()
          onMouseLeave?.(e)
        }}
        {...rest}
      >
        <Icon size={16} aria-hidden="true" />
      </button>
      {tip.node}
    </>
  )
}
