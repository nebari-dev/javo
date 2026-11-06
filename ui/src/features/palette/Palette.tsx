import * as Dialog from '@radix-ui/react-dialog'
import { Command, useCommandState } from 'cmdk'
import { ArrowLeft, Search, type LucideIcon } from 'lucide-react'
import { createContext, useContext, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Kbd } from '../../components/Kbd'
import { match, matchItem } from './match'

/** One row of the palette. */
export interface PaletteItem {
  /** Stable and unique across sections; recents are kept by it. */
  id: string
  label: string
  /** Extra words that find it (template, owner, kind); the label always counts. */
  keywords?: string[]
  icon: ReactNode
  /** Quiet text after the label, such as "from Bob". */
  detail?: string
  /** Right side: a status pill or a short note. */
  meta?: ReactNode
  /** Enter. Left out for a row that cannot run now (a stopped app shared with the user); its meta says why. */
  run?: () => void
  /** More things to do with the row (PaletteAction entries), listed by Tab or the right arrow; rendered only while listed. */
  actions?: ReactNode
}

export interface PaletteSection {
  heading: string
  items: PaletteItem[]
  /** Shown first and never filtered: for rows made from the whole query, such as asking a model about it. */
  pinned?: boolean
}

interface PaletteControls {
  /** Close the palette (after an action ran). */
  close: () => void
  /** Where a panel renders, and whether one is showing (the list hides meanwhile). */
  slot: HTMLElement | null
  showing: (on: boolean) => void
}

const Controls = createContext<PaletteControls>({ close: () => {}, slot: null, showing: () => {} })

/** For an item's actions list: close the palette when an action is done. */
export const usePalette = () => useContext(Controls).close

/**
 * Content shown in place of the list while it is mounted, such as a
 * confirmation. It stays part of the actions list that renders it, so it
 * keeps that component's state and hooks.
 */
export function PalettePanel({ children }: { children: ReactNode }) {
  const { slot, showing } = useContext(Controls)
  useLayoutEffect(() => {
    showing(true)
    return () => showing(false)
  }, [showing])
  return slot ? createPortal(children, slot) : null
}

/** One entry of an item's actions list. */
export function PaletteAction({ icon: Icon, label, onSelect }: { icon: LucideIcon; label: string; onSelect: () => void }) {
  return (
    <Command.Item value={label} onSelect={onSelect}>
      <span className="pal-icon" aria-hidden="true">
        <Icon size={16} />
      </span>
      <span className="pal-label">{label}</span>
    </Command.Item>
  )
}

type Ranges = [number, number][]

/**
 * The sections that match a query, rows best first and sections by their best
 * row; pinned sections come first and whole. Also the matched ranges of each
 * row's label, to mark them.
 */
function matching(sections: PaletteSection[], search: string): { sections: PaletteSection[]; hits: Map<string, Ranges> } {
  const hits = new Map<string, Ranges>()
  const scored = sections.map((s) => {
    if (s.pinned) return { s, best: Infinity }
    const rows = s.items.flatMap((item) => {
      const m = matchItem(item.label, item.keywords ?? [], search)
      if (!m) return []
      hits.set(item.id, m.ranges)
      return [{ item, score: m.score }]
    })
    rows.sort((a, b) => b.score - a.score)
    return { s: { ...s, items: rows.map((r) => r.item) }, best: rows[0]?.score ?? 0 }
  })
  const shown = scored
    .filter((x) => x.s.items.length > 0)
    .sort((a, b) => b.best - a.best)
    .map((x) => x.s)
  return { sections: shown, hits }
}

// The label with its matched letters marked.
function Marked({ text, ranges }: { text: string; ranges?: Ranges }) {
  if (!ranges?.length) return <span className="pal-title">{text}</span>
  const parts: ReactNode[] = []
  let at = 0
  ranges.forEach(([a, b], i) => {
    if (a > at) parts.push(text.slice(at, a))
    parts.push(<mark key={i}>{text.slice(a, b)}</mark>)
    at = b
  })
  parts.push(text.slice(at))
  return <span className="pal-title">{parts}</span>
}

// For the actions list, which cmdk filters: the same matching, as 0 (hidden) or a rank.
const actionFilter = (value: string, search: string) => (match(value, search)?.score ?? 0) / 1000

function Row({ item, ranges, onRun }: { item: PaletteItem; ranges?: Ranges; onRun: (item: PaletteItem) => void }) {
  return (
    <Command.Item value={item.id} onSelect={() => onRun(item)}>
      <span className="pal-icon" aria-hidden="true">
        {item.icon}
      </span>
      <span className="pal-label">
        <Marked text={item.label} ranges={ranges} />
        {item.detail && <span className="pal-detail">{item.detail}</span>}
      </span>
      {item.meta && <span className="pal-meta">{item.meta}</span>}
    </Command.Item>
  )
}

// Move and Run only mean something while the list has entries.
function ListHints({ run }: { run: string }) {
  const count = useCommandState((s) => s.filtered.count)
  if (count === 0) return null
  return (
    <>
      <Hint keys={['↑', '↓']}>Move</Hint>
      <Hint keys={['↵']}>{run}</Hint>
    </>
  )
}

function Hint({ keys, children }: { keys: string[]; children: ReactNode }) {
  return (
    <span className="pal-hint">
      {keys.map((k) => (
        <Kbd key={k}>{k}</Kbd>
      ))}
      {children}
    </span>
  )
}

/**
 * The command palette: a modal dialog with one search field over grouped
 * results. With nothing typed it shows `idle` (recent items and suggestions);
 * typing filters `sections` fuzzily. Sections are plain data, so another one
 * (such as answers from a model) is added by passing it in.
 */
export function Palette(props: {
  onClose: () => void
  /** Focus goes here on close when nothing in the page had it before. */
  fallbackFocus: () => HTMLElement | null
  sections: PaletteSection[]
  idle: PaletteSection[]
  /** Called when an item runs or its actions open, to remember it as recent. */
  onUse: (item: PaletteItem) => void
  /**
   * A section made from the whole query, shown first and unfiltered (such as
   * asking a model about it). Called on every render, also with an empty
   * query, so an open item of it keeps its text.
   */
  asking?: (query: string) => PaletteSection | undefined
  /** The search box also takes a request in plain words (asking can answer one). */
  describes?: boolean
}) {
  const [search, setSearch] = useState('')
  const [value, setValue] = useState('')
  // The item whose actions are listed, by id, so it follows fresh data.
  const [pageId, setPageId] = useState<string | null>(null)
  const [panel, setPanel] = useState(false)
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  const before = useRef<Element | null>(typeof document === 'undefined' ? null : document.activeElement)
  const input = useRef<HTMLInputElement>(null)

  const query = search.trim()
  const asked = props.asking?.(query)
  const sections = asked ? [{ ...asked, pinned: true }, ...props.sections] : props.sections
  const found = query ? matching(sections, query) : { sections: props.idle, hits: undefined }
  const all = [...props.idle, ...sections].flatMap((s) => s.items)
  const page = (pageId && all.find((i) => i.id === pageId)) || null
  const current = page ? undefined : all.find((i) => i.id === value)

  const openActions = (item: PaletteItem) => {
    props.onUse(item)
    setPageId(item.id)
    setSearch('')
    setValue('')
  }
  const back = () => {
    setPageId(null)
    setSearch('')
    input.current?.focus()
  }
  const run = (item: PaletteItem) => {
    if (!item.run) {
      if (item.actions) openActions(item)
      return
    }
    props.onUse(item)
    props.onClose()
    item.run()
  }
  const showing = useRef((on: boolean) => {
    setPanel(on)
    if (!on) input.current?.focus()
  }).current

  return (
    <Dialog.Root open onOpenChange={(o) => !o && props.onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="pal-overlay" />
        <Dialog.Content
          className="pal"
          aria-modal="true"
          aria-describedby={undefined}
          onEscapeKeyDown={(e) => {
            // A panel handles its own Escape (a confirmation cancels); in the actions list Escape goes back.
            if (panel || page) e.preventDefault()
            if (!panel && page) back()
          }}
          onCloseAutoFocus={(e) => {
            e.preventDefault()
            const prev = before.current
            const target = prev instanceof HTMLElement && prev !== document.body && prev.isConnected ? prev : props.fallbackFocus()
            target?.focus()
          }}
        >
          <Dialog.Title className="sr-only">Search and commands</Dialog.Title>
          <Controls.Provider value={{ close: props.onClose, slot, showing }}>
            <Command label="Search" shouldFilter={!!page} filter={actionFilter} value={value} onValueChange={setValue} vimBindings={false} loop>
              <div className="pal-search">
                {page ? (
                  <button type="button" className="pal-back" aria-label="Back to results" onClick={back}>
                    <ArrowLeft size={16} aria-hidden="true" />
                  </button>
                ) : (
                  <Search size={16} aria-hidden="true" className="pal-search-icon" />
                )}
                <Command.Input
                  ref={input}
                  value={search}
                  onValueChange={setSearch}
                  placeholder={page ? 'Filter actions' : props.describes ? 'Search, or describe what you want' : 'Search apps, jobs, actions'}
                  onKeyDown={(e) => {
                    const atEnd = e.currentTarget.selectionStart === e.currentTarget.value.length
                    if (!page && current?.actions && (e.key === 'Tab' || (e.key === 'ArrowRight' && atEnd))) {
                      e.preventDefault()
                      openActions(current)
                    } else if (page && e.key === 'Backspace' && search === '') {
                      e.preventDefault()
                      back()
                    }
                  }}
                />
              </div>
              <div className="pal-panel" ref={setSlot} hidden={!panel} />
              <Command.List className="pal-list" hidden={panel} label="Results">
                {page?.actions ? (
                  <Command.Group heading={page.label}>{page.actions}</Command.Group>
                ) : (
                  <>
                    <Command.Empty className="pal-empty">
                      <b>No matches for “{query}”</b>
                      <span>Try an app name, a template such as Streamlit, or a page such as Settings.</span>
                    </Command.Empty>
                    {found.sections
                      .filter((s) => s.items.length > 0)
                      .map((s) => (
                        <Command.Group key={s.heading} heading={s.heading}>
                          {s.items.map((item) => (
                            <Row key={item.id} item={item} ranges={found.hits?.get(item.id)} onRun={run} />
                          ))}
                        </Command.Group>
                      ))}
                  </>
                )}
              </Command.List>
              <div className="pal-foot">
                {!panel && <ListHints run={page ? 'Run' : 'Open'} />}
                {current?.actions && (
                  <button type="button" className="pal-hint pal-hint-btn" onClick={() => openActions(current)}>
                    <Kbd>→</Kbd>
                    Actions
                  </button>
                )}
                <span className="pal-foot-end">
                  <Hint keys={['Esc']}>{panel ? 'Cancel' : page ? 'Back' : 'Close'}</Hint>
                </span>
              </div>
            </Command>
          </Controls.Provider>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
