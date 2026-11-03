import { useSyncExternalStore } from 'react'
import { readStored, writeStored } from './storage'

export type Theme = 'light' | 'system' | 'dark'

const KEY = 'javo.theme'

// Only used when storage refuses the write (private windows), so the choice still holds for this tab.
let unsaved: Theme | null = null
const listeners = new Set<() => void>()

export function loadTheme(): Theme {
  const v = unsaved ?? readStored(KEY)
  return v === 'light' || v === 'dark' ? v : 'system'
}

export function applyTheme(theme: Theme): void {
  const root = document.documentElement
  if (theme === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', theme)
}

/** Apply, remember and announce a theme; every useTheme caller sees it. */
export function setTheme(theme: Theme): void {
  writeStored(KEY, theme)
  unsaved = readStored(KEY) === theme ? null : theme
  applyTheme(theme)
  listeners.forEach((l) => l())
}

/** The chosen theme, shared by the theme switch and the command palette. */
export function useTheme(): Theme {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    loadTheme,
  )
}
