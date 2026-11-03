import { Plus } from 'lucide-react'
import { Link } from 'react-router'

interface EmptyStateProps {
  title: string
  body: string
  action?: { label: string; to: string }
}

// Original composition: a soft tile holding two app windows and a sparkle.
// Every color comes from theme tokens (see .es-* in components.css).
function Illustration() {
  return (
    <svg className="es-art" viewBox="0 0 160 120" width="160" height="120" aria-hidden="true" focusable="false">
      <rect className="es-tile" x="12" y="10" width="136" height="100" rx="26" />
      <g className="es-win es-back">
        <rect x="30" y="30" width="72" height="52" rx="10" />
        <path d="M30 44h72" />
        <circle cx="39" cy="37" r="2" />
        <circle cx="46" cy="37" r="2" />
      </g>
      <g className="es-win es-front">
        <rect x="58" y="48" width="74" height="50" rx="10" />
        <path d="M58 62h74" />
        <circle cx="67" cy="55" r="2" />
        <circle cx="74" cy="55" r="2" />
        <rect className="es-line" x="68" y="70" width="36" height="5" rx="2.5" />
        <rect className="es-line soft" x="68" y="80" width="50" height="5" rx="2.5" />
      </g>
      <path className="es-spark" d="M128 18c1.2 6 3 7.8 9 9-6 1.2-7.8 3-9 9-1.2-6-3-7.8-9-9 6-1.2 7.8-3 9-9z" />
      <circle className="es-spark" cx="24" cy="96" r="3" />
    </svg>
  )
}

export function EmptyState({ title, body, action }: EmptyStateProps) {
  return (
    <div className="empty-state">
      <Illustration />
      <div className="es-text">
        <h3 className="es-title">{title}</h3>
        <p className="es-body">{body}</p>
      </div>
      {action && (
        <Link to={action.to} className="btn primary">
          <Plus size={16} aria-hidden="true" />
          {action.label}
        </Link>
      )}
    </div>
  )
}
