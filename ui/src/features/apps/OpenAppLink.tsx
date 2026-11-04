import { ExternalLink } from 'lucide-react'
import type { ReactNode } from 'react'

/** The link that opens a running app: always in a new tab, so this page stays where it was. */
export function OpenAppLink({ href, className, label, children }: { href: string; className: string; label?: string; children: ReactNode }) {
  return (
    <a className={className} href={href} target="_blank" rel="noopener" aria-label={label}>
      <ExternalLink size={16} aria-hidden="true" />
      {children}
    </a>
  )
}
