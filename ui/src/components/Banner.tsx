import { AlertTriangle, CircleCheck, Info, XCircle, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'

export type BannerTone = 'ok' | 'info' | 'warn' | 'err'

const ICONS: Record<BannerTone, LucideIcon> = { ok: CircleCheck, info: Info, warn: AlertTriangle, err: XCircle }

interface BannerProps {
  tone: BannerTone
  title: ReactNode
  children?: ReactNode
  actions?: ReactNode
  className?: string
}

export function Banner({ tone, title, children, actions, className }: BannerProps) {
  const Icon = ICONS[tone]
  return (
    <div className={['banner', tone, className ?? ''].filter(Boolean).join(' ')} role={tone === 'err' ? 'alert' : 'status'}>
      <Icon size={20} aria-hidden="true" className="b-ico" />
      <div className="b-body">
        <div className="b-title">{title}</div>
        {children && <div className="b-text">{children}</div>}
      </div>
      {actions && <div className="b-actions">{actions}</div>}
    </div>
  )
}
