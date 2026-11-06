import { AlertTriangle, CircleCheck, Info, XCircle, type LucideIcon } from 'lucide-react'
import { useId, type ReactNode } from 'react'

export type BannerTone = 'ok' | 'info' | 'warn' | 'err'

const ICONS: Record<BannerTone, LucideIcon> = { ok: CircleCheck, info: Info, warn: AlertTriangle, err: XCircle }

interface BannerProps {
  tone: BannerTone
  title: ReactNode
  children?: ReactNode
  actions?: ReactNode
  className?: string
  /** Drawn in place of the tone's icon, such as the logo of what the banner is about. */
  icon?: ReactNode
}

export function Banner({ tone, title, children, actions, className, icon }: BannerProps) {
  const Icon = ICONS[tone]
  const titleId = useId()
  return (
    <div className={['banner', tone, className ?? ''].filter(Boolean).join(' ')} role={tone === 'err' ? 'alert' : 'status'} aria-labelledby={titleId}>
      {icon ?? <Icon size={20} aria-hidden="true" className="b-ico" />}
      <div className="b-body">
        <div className="b-title" id={titleId}>
          {title}
        </div>
        {children && <div className="b-text">{children}</div>}
      </div>
      {actions && <div className="b-actions">{actions}</div>}
    </div>
  )
}
