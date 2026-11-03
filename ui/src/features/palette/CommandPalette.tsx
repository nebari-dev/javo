import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { SearchTrigger } from './SearchTrigger'

// The palette and its libraries load the first time it opens.
const OpenPalette = lazy(() => import('./OpenPalette'))

/** The top bar's search field and the palette it opens; Cmd+K or Ctrl+K toggles it from anywhere. */
export function CommandPalette() {
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen((o) => !o)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
  return (
    <>
      <SearchTrigger ref={trigger} onOpen={() => setOpen(true)} />
      {open && (
        <Suspense fallback={null}>
          <OpenPalette onClose={() => setOpen(false)} fallbackFocus={() => trigger.current} />
        </Suspense>
      )}
    </>
  )
}

