import type { LucideIcon } from 'lucide-react'
import type { ButtonHTMLAttributes, Ref } from 'react'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'primary' | 'ghost' | 'danger'
  size?: 'md' | 'sm'
  icon?: LucideIcon
  ref?: Ref<HTMLButtonElement>
}

export function Button({ variant = 'default', size = 'md', icon: Icon, className, children, type = 'button', ...rest }: ButtonProps) {
  const classes = ['btn', variant === 'default' ? '' : variant, size === 'sm' ? 'sm' : '', Icon && !children ? 'icon' : '', className ?? '']
    .filter(Boolean)
    .join(' ')
  return (
    <button type={type} className={classes} {...rest}>
      {Icon && <Icon size={16} aria-hidden="true" />}
      {children}
    </button>
  )
}
