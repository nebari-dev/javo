import { ChevronRight } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'

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
      <button
        type="button"
        className="more-toggle"
        aria-expanded={isOpen}
        aria-controls={regionId}
        onClick={() => setOpen(!isOpen)}
      >
        <ChevronRight size={15} aria-hidden="true" className={`more-chevron${isOpen ? ' open' : ''}`} />
        {isOpen ? open : closed}
      </button>
      <div id={regionId} className="more-body">
        {isOpen && children}
      </div>
    </div>
  )
}
