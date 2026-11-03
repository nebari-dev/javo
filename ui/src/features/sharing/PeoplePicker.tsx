import { useCombobox } from 'downshift'
import { Users } from 'lucide-react'
import { useState } from 'react'
import { useDirectory } from '../../lib/queries'

export type Picked = { kind: 'user'; id: string; label: string; email: string } | { kind: 'group'; value: string; label: string }

interface PeoplePickerProps {
  /** People (by id) and groups (by value) already added; they are left out of the results. */
  taken: string[]
  onPick: (pick: Picked) => void
  /** True while a change is being saved: the next pick must build on the saved list. */
  disabled?: boolean
}

/** Search the directory for people and the caller's own groups. Arrow keys and Enter pick, Escape closes. */
export function PeoplePicker({ taken, onPick, disabled }: PeoplePickerProps) {
  const [text, setText] = useState('')
  const dir = useDirectory(text)
  const items: Picked[] = [
    ...(dir.data?.people ?? []).map((p): Picked => ({ kind: 'user', id: p.id, label: p.displayName || p.email, email: p.email })),
    ...(dir.data?.groups ?? []).map((g): Picked => ({ kind: 'group', value: g.value, label: g.value })),
  ].filter((i) => !taken.includes(i.kind === 'user' ? i.id : i.value))

  const { isOpen, getInputProps, getMenuProps, getItemProps, getLabelProps, highlightedIndex } = useCombobox<Picked>({
    items,
    inputValue: text,
    selectedItem: null,
    itemToString: (i) => i?.label ?? '',
    onInputValueChange: ({ inputValue }) => setText(inputValue),
    onSelectedItemChange: ({ selectedItem }) => {
      if (!selectedItem) return
      setText('')
      onPick(selectedItem)
    },
  })

  return (
    <div className="field combo">
      <label {...getLabelProps()}>Add people or groups</label>
      <input {...getInputProps({ disabled })} className="input" placeholder="Name, email, or group" autoComplete="off" />
      <span className="hint">Groups come from your sign-in provider. Changes apply within a minute.</span>
      <ul {...getMenuProps()} className="picker-menu" hidden={!isOpen || items.length === 0}>
        {isOpen &&
          items.map((item, index) => {
            const key = item.kind === 'user' ? `u:${item.id}` : `g:${item.value}`
            return (
              <li key={key} {...getItemProps({ item, index })} className={highlightedIndex === index ? 'on' : ''}>
                {item.kind === 'group' && <Users size={15} aria-hidden="true" />}
                <span>{item.label}</span>
                {item.kind === 'user' && <span className="faint small">{item.email}</span>}
              </li>
            )
          })}
      </ul>
    </div>
  )
}
