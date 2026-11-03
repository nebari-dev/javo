import { LogOut, Settings } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Avatar } from '../components/Avatar'

interface UserMenuProps {
  name: string
  picture?: string
  onSignOut: () => void
}

/** The avatar button at the top right and its account menu (name, Settings, Sign out). */
export function UserMenu({ name, picture, onSignOut }: UserMenuProps) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const first = useRef<HTMLAnchorElement>(null)

  useEffect(() => {
    if (!open) return
    first.current?.focus()
    const onPointer = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setOpen(false)
      trigger.current?.focus()
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div className="user-menu" ref={root}>
      <button
        ref={trigger}
        type="button"
        className="avatar-btn"
        aria-label={`Account: ${name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <Avatar name={name} picture={picture} />
      </button>
      {open && (
        <div className="menu" role="menu" aria-label="Account">
          <div className="menu-head" role="presentation">
            <span className="menu-avatar" aria-hidden="true">
              <Avatar name={name} picture={picture} />
            </span>
            <span className="menu-name">{name}</span>
          </div>
          <Link ref={first} to="/settings" role="menuitem" className="menu-item" onClick={() => setOpen(false)}>
            <Settings size={16} aria-hidden="true" />
            Settings
          </Link>
          <button type="button" role="menuitem" className="menu-item" onClick={onSignOut}>
            <LogOut size={16} aria-hidden="true" />
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}
