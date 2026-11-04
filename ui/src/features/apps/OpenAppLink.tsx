import { ExternalLink } from 'lucide-react'
import type { ReactNode } from 'react'

/** The link that opens a running app: always in a new tab, so this page stays where it was. */
/** icon replaces the external-link mark, e.g. with the app's own logo. */
export function OpenAppLink({ href, className, label, icon, children }: { href: string; className: string; label?: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <a className={className} href={href} target="_blank" rel="noopener" aria-label={label}>
      {icon ?? <ExternalLink size={16} aria-hidden="true" />}
      {children}
    </a>
  )
}
