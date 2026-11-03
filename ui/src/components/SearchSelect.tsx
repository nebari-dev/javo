import { useCombobox } from 'downshift'
import { ChevronDown } from 'lucide-react'
import { useState } from 'react'

interface SearchSelectProps {
  id?: string
  label: string
  /** Shown as the visible label; the input is named by it too. */
  options: string[]
  value: string
  onChange: (value: string) => void
  invalid?: boolean
  describedBy?: string
}

/** Lowercase, with spaces, underscores, slashes and dashes treated alike. */
const fold = (s: string) => s.toLowerCase().replace(/[\s_/-]+/g, ' ')

/**
 * A select for long lists: type any part of an option to narrow it, then pick
 * with the mouse or the arrow keys. Leaving the field keeps the last pick.
 */
export function SearchSelect({ id, label, options, value, onChange, invalid, describedBy }: SearchSelectProps) {
  // Downshift owns the typed text; this is only the filter it last reported.
  const [query, setQuery] = useState<string | null>(null)
  const items = query === null ? options : options.filter((o) => fold(o).includes(fold(query)))
  const { isOpen, getInputProps, getMenuProps, getItemProps, getLabelProps, getToggleButtonProps, highlightedIndex } = useCombobox<string>({
    id,
    items,
    selectedItem: value,
    onInputValueChange: ({ inputValue, type }) => {
      // Only typing filters; picking or leaving shows the full list next time.
      setQuery(type === useCombobox.stateChangeTypes.InputChange ? (inputValue ?? '') : null)
    },
    onSelectedItemChange: ({ selectedItem }) => {
      if (selectedItem) onChange(selectedItem)
    },
  })
  return (
    <div className="field combo">
      <label {...getLabelProps()}>{label}</label>
      <span className="select">
        <input
          {...getInputProps({
            'aria-describedby': describedBy,
            'aria-invalid': invalid || undefined,
            // Typing replaces the current choice instead of appending to it.
            onFocus: (e) => e.currentTarget.select(),
          })}
          className={`input${invalid ? ' err' : ''}`}
          autoComplete="off"
          spellCheck={false}
        />
        <button type="button" {...getToggleButtonProps()} className="select-toggle" aria-label={`Show all ${label.toLowerCase()} options`} tabIndex={-1}>
          <ChevronDown size={16} aria-hidden="true" />
        </button>
        <ul {...getMenuProps()} className="picker-menu scroll" hidden={!isOpen || items.length === 0}>
          {isOpen &&
            items.map((item, index) => (
              <li key={item} {...getItemProps({ item, index })} className={highlightedIndex === index ? 'on' : ''}>
                <span>{item}</span>
              </li>
            ))}
        </ul>
      </span>
    </div>
  )
}
