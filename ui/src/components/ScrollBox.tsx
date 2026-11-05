import type { ReactNode } from 'react'

/**
 * A list that shows everything and scrolls in place past a set height, instead
 * of cutting the list short behind a "See all" link. It is a named region and a
 * tab stop, so the keyboard can scroll it too.
 */
export function ScrollBox({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    <div role="region" aria-label={label} tabIndex={0} className={['scroll-box', className ?? ''].filter(Boolean).join(' ')}>
      {children}
    </div>
  )
}
