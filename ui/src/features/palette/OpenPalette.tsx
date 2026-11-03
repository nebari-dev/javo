import { useEffect, useState } from 'react'
import { useAskSection } from './AskAi'
import { Palette, type PaletteItem, type PaletteSection } from './Palette'
import { useCreateItems, usePaletteSections } from './items'
import { useMe } from '../../lib/queries'
import { readStored, writeStored } from '../../lib/storage'

const RECENT_MAX = 5
// Shown with nothing typed, after the recent items, when they exist.
const SUGGESTED = ['do:new-app', 'do:new-job', 'go:templates', 'go:settings']

const recentKey = (user: string) => `javo.palette.recent.${user}`

function readRecent(user: string): string[] {
  try {
    const v: unknown = JSON.parse(readStored(recentKey(user)) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

// Mounted only while open, so its queries run only when someone searches.
export default function OpenPalette({ onClose, fallbackFocus }: { onClose: () => void; fallbackFocus: () => HTMLElement | null }) {
  const sections = usePaletteSections()
  const create = useCreateItems()
  const asking = useAskSection()
  const user = useMe().data?.id ?? ''
  const [recent, setRecent] = useState(() => readRecent(user))
  useEffect(() => {
    setRecent(readRecent(user))
  }, [user])

  // Create items show only with nothing typed; searching finds the same templates as New X app.
  const byId = new Map([...sections.flatMap((s) => s.items), ...create].map((i) => [i.id, i]))
  const pick = (ids: string[]) => ids.flatMap((id) => byId.get(id) ?? [])
  const idle: PaletteSection[] = [
    { heading: 'Recent', items: pick(recent) },
    { heading: 'Suggestions', items: pick(SUGGESTED.filter((id) => !recent.includes(id))) },
    { heading: 'Create an app', items: create.filter((i) => !recent.includes(i.id)) },
  ]
  const use = (item: PaletteItem) => {
    const next = [item.id, ...recent.filter((id) => id !== item.id)].slice(0, RECENT_MAX)
    setRecent(next)
    if (user) writeStored(recentKey(user), JSON.stringify(next))
  }
  return <Palette onClose={onClose} fallbackFocus={fallbackFocus} sections={sections} idle={idle} onUse={use} asking={asking} />
}
