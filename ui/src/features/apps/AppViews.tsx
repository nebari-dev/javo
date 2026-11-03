import { LayoutGrid, List } from 'lucide-react'
import { useState } from 'react'
import { readStored, writeStored } from '../../lib/storage'
import { IconSwitch, type IconOption } from '../../components/IconSwitch'

export type AppsView = 'cards' | 'list'

const KEY = 'javo.appsView'

const OPTIONS: IconOption<AppsView>[] = [
  { value: 'cards', label: 'Cards', icon: LayoutGrid },
  { value: 'list', label: 'List', icon: List },
]

/** The apps view chosen in this browser; cards unless list was saved. */
export function useAppsView(): [AppsView, (v: AppsView) => void] {
  const [view, setView] = useState<AppsView>(() => (readStored(KEY) === 'list' ? 'list' : 'cards'))
  const choose = (v: AppsView) => {
    setView(v)
    writeStored(KEY, v)
  }
  return [view, choose]
}

export function ViewSwitch({ view, onChange }: { view: AppsView; onChange: (v: AppsView) => void }) {
  return <IconSwitch label="Apps view" options={OPTIONS} value={view} onChange={onChange} />
}

/** The apps as a card grid or a table. */
