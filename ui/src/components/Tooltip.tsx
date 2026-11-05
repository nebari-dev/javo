import { useEffect, useRef, useState, type FocusEvent, type KeyboardEvent, type MouseEvent } from 'react'
import { createPortal } from 'react-dom'

const GAP = 6
const EDGE = 8

/**
 * The tooltip of an icon-only control. It is drawn in a portal on the page
 * body at fixed coordinates, so no scrolling or clipping container around the
 * control can cut it off. It shows on hover and keyboard focus after a short
 * delay, sits below the control (above when there is no room), and Escape,
 * leaving, blur or any scroll hides it (WCAG 1.4.13).
 */
export function useTooltip(label: string) {
  const [at, setAt] = useState<{ top: number; left: number } | null>(null)
  const timer = useRef<number>(undefined)
  const tip = useRef<HTMLSpanElement>(null)
  const anchor = useRef<HTMLElement | null>(null)

  const hide = () => {
    window.clearTimeout(timer.current)
    setAt(null)
  }
  const show = (el: HTMLElement) => {
    anchor.current = el
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      const r = el.getBoundingClientRect()
      setAt({ top: r.bottom + GAP, left: r.right })
    }, 250)
  }

  // Once drawn, its size is known: keep it inside the viewport.
  useEffect(() => {
    const t = tip.current
    const a = anchor.current
    if (!at || !t || !a) return
    const r = a.getBoundingClientRect()
    const w = t.offsetWidth
    const h = t.offsetHeight
    const top = r.bottom + GAP + h > window.innerHeight - EDGE ? r.top - GAP - h : r.bottom + GAP
    const left = Math.min(Math.max(EDGE, r.right - w), window.innerWidth - EDGE - w)
    if (top !== at.top || left !== at.left) setAt({ top, left })
  }, [at])

  useEffect(() => {
    if (!at) return
    window.addEventListener('scroll', hide, true)
    window.addEventListener('resize', hide)
    return () => {
      window.removeEventListener('scroll', hide, true)
      window.removeEventListener('resize', hide)
    }
  }, [at])

  useEffect(() => () => window.clearTimeout(timer.current), [])

  const handlers = {
    onMouseEnter: (e: MouseEvent<HTMLElement>) => show(e.currentTarget),
    onMouseLeave: hide,
    onFocus: (e: FocusEvent<HTMLElement>) => {
      if (e.currentTarget.matches(':focus-visible')) show(e.currentTarget)
    },
    onBlur: hide,
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
      if (e.key === 'Escape') hide()
    },
  }
  const node = at
    ? createPortal(
        <span ref={tip} role="tooltip" className="tooltip" style={{ top: at.top, left: at.left }}>
          {label}
        </span>,
        document.body,
      )
    : null
  return { handlers, node }
}
