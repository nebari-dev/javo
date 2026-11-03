import { Monitor, Moon, Sun } from 'lucide-react'
import { useEffect } from 'react'
import { applyTheme, setTheme, useTheme, type Theme } from '../lib/theme'

const MODES: Record<Theme, { label: string; icon: typeof Sun; next: Theme }> = {
  system: { label: 'System', icon: Monitor, next: 'light' },
  light: { label: 'Light', icon: Sun, next: 'dark' },
  dark: { label: 'Dark', icon: Moon, next: 'system' },
}

/** One button that shows the current appearance and moves to the next on click. */
export function ThemeSwitch() {
  const theme = useTheme()
  useEffect(() => {
    applyTheme(theme)
  }, [theme])
  const { label, icon: Icon, next } = MODES[theme]
  return (
    <button
      type="button"
      className="icon-btn"
      aria-label={`Appearance: ${label}`}
      data-tip={`Appearance: ${label}`}
      onClick={() => setTheme(next)}
    >
      <Icon size={18} strokeWidth={1.75} aria-hidden="true" />
    </button>
  )
}
