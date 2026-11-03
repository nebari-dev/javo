import { Search } from 'lucide-react'
import type { Ref } from 'react'
import { Kbd } from '../../components/Kbd'

const isMac = () => /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent)

/** The keys for the palette shortcut on this computer, one key each. */
function shortcutKeys(): string[] {
  return isMac() ? ['⌘', 'K'] : ['Ctrl', 'K']
}

/** The top bar's search field: opens the palette; an icon button on phones. */
export function SearchTrigger({ onOpen, ref }: { onOpen: () => void; ref?: Ref<HTMLButtonElement> }) {
  const keys = shortcutKeys()
  return (
    <button
      ref={ref}
      type="button"
      className="search-trigger"
      aria-label="Search"
      aria-haspopup="dialog"
      aria-keyshortcuts="Meta+K Control+K"
      onClick={onOpen}
    >
      <Search size={16} aria-hidden="true" />
      <span className="search-trigger-text">Search</span>
      <span className="search-trigger-keys" aria-hidden="true">
        {keys.map((k) => (
          <Kbd key={k}>{k}</Kbd>
        ))}
      </span>
    </button>
  )
}
