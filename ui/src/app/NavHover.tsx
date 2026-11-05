import { useRef, type PointerEvent, type ReactNode } from 'react'

/**
 * A nav whose hover tint is one shape that slides from item to item as the
 * pointer moves along the links, instead of each link lighting on its own.
 * Entering the nav from outside places the shape without sliding; leaving it
 * fades the shape out. Reduced motion turns the slide off (CSS).
 */
export function SlidingNav({ className, label, children }: { className: string; label: string; children: ReactNode }) {
  const tint = useRef<HTMLSpanElement>(null)
  const shown = useRef(false)
  const move = (e: PointerEvent<HTMLElement>) => {
    const el = tint.current
    const link = (e.target as Element).closest('a')
    if (!el || !link || !e.currentTarget.contains(link)) return
    if (!shown.current) el.dataset.jump = ''
    el.style.transform = `translateY(${(link as HTMLElement).offsetTop}px)`
    el.style.height = `${(link as HTMLElement).offsetHeight}px`
    el.style.opacity = '1'
    if (!shown.current) {
      shown.current = true
      void el.offsetHeight
      delete el.dataset.jump
    }
  }
  const leave = () => {
    shown.current = false
    if (tint.current) tint.current.style.opacity = '0'
  }
  return (
    <nav className={`${className} sliding`} aria-label={label} onPointerOver={move} onPointerLeave={leave}>
      <span ref={tint} className="nav-tint" aria-hidden="true" />
      {children}
    </nav>
  )
}
