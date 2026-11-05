import { ChevronRight } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'

interface MoreToggleProps {
  open: boolean
  onToggle: () => void
  /** The id of the region it shows and hides. */
  controls?: string
  className?: string
  children: ReactNode
}

/**
 * The one disclosure header: a chevron first, then the label. Opening only
 * turns the chevron; the label may change words but nothing moves.
 */
export function MoreToggle({ open, onToggle, controls, className, children }: MoreToggleProps) {
  return (
    <button type="button" className={className ? `more-toggle ${className}` : 'more-toggle'} aria-expanded={open} aria-controls={controls} onClick={onToggle}>
      <ChevronRight size={15} aria-hidden="true" className={`more-chevron${open ? ' open' : ''}`} />
      {children}
    </button>
  )
}

interface MoreProps {
  closed: ReactNode
  open: ReactNode
  defaultOpen?: boolean
  children: ReactNode
}

/** The single progressive-disclosure control: details stay hidden until the user asks. */
export function More({ closed, open, defaultOpen = false, children }: MoreProps) {
  const [isOpen, setOpen] = useState(defaultOpen)
  const regionId = useId()
  return (
    <div className="more">
      <MoreToggle open={isOpen} onToggle={() => setOpen(!isOpen)} controls={regionId}>
        {isOpen ? open : closed}
      </MoreToggle>
      <div id={regionId} className="more-body">
        {isOpen && children}
      </div>
    </div>
  )
}
