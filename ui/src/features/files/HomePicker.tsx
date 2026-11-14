import { useEffect, useState, type ReactNode } from 'react'
import { formatSize, groupPlace, HOME_PLACE, type HomeEntry } from '../../lib/files'
import { ApiError, problemText } from '../../lib/http'
import { useHomeFolder, useMe, useStorage } from '../../lib/queries'
import { readStored, writeStored } from '../../lib/storage'
import { Banner } from '../../components/Banner'
import { FailureCard } from '../logs/FailureCard'
import { FilePicker, type PickerEntry, type PickerExtras } from './FilePicker'

function join(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

function toEntry(dir: string, e: HomeEntry): PickerEntry {
  return {
    name: e.name,
    path: join(dir, e.name),
    kind: e.kind === 'folder' ? 'dir' : 'file',
    modified: formatDate(e.modified),
    size: e.kind === 'folder' ? undefined : formatSize(e.size),
  }
}

const hiddenKey = (user: string) => `javo.picker.hidden.${user}`

function parent(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i)
}

const SAMPLE_TAG = 'Sample'
const SAMPLE_NEW_TAG = 'Sample, copied here when the app starts'
const SAMPLE_DIR_TAG = 'Made when the app starts'

/**
 * Marks the step towards the sample in one listing of dir: the sample itself
 * or the folder that leads to it. The sample is copied to the home when an
 * app first starts, so a step that is not there yet is listed as where it will
 * be, among the entries of its kind.
 */
function withSample(dir: string, entries: PickerEntry[], sample: string, sampleKind: PickerEntry['kind']): PickerEntry[] {
  if (dir && !sample.startsWith(`${dir}/`)) return entries
  const name = sample.slice(dir ? dir.length + 1 : 0).split('/')[0]
  const path = join(dir, name)
  const last = path === sample
  if (entries.some((e) => e.name === name)) {
    return last ? entries.map((e) => (e.name === name ? { ...e, tag: SAMPLE_TAG } : e)) : entries
  }
  const kind = last ? sampleKind : 'dir'
  const step: PickerEntry = { name, path, kind, tag: last ? SAMPLE_NEW_TAG : SAMPLE_DIR_TAG }
  const at = entries.findIndex((e) => (e.kind === kind && e.name.localeCompare(name) > 0) || (kind === 'dir' && e.kind === 'file'))
  return at < 0 ? [...entries, step] : [...entries.slice(0, at), step, ...entries.slice(at)]
}

/** How long a folder may take to open before the picker says why it is waiting. */
const SLOW_MS = 3000

/** True once active has held for ms with the same key; a new key starts the wait again. */
function useAfter(active: boolean, ms: number, key: string): boolean {
  const [doneFor, setDoneFor] = useState<string | null>(null)
  useEffect(() => {
    if (!active) return
    const t = setTimeout(() => setDoneFor(key), ms)
    return () => clearTimeout(t)
  }, [active, ms, key])
  return active && doneFor === key
}

/**
 * The file picker over the user's home folder and, when choosing a file, the
 * folders of the user's groups, with the API's own words for its waiting and
 * failure states. An app or a job runs a file of its owner's home, so group
 * folders can be looked through but not chosen from.
 */
export function HomePicker({
  extensions,
  acceptReason,
  folders = false,
  filesToo = false,
  sample,
  onChoose,
  onClose,
  ...extras
}: PickerExtras & {
  extensions: string[]
  acceptReason: string
  /** Choose a folder instead of a file. */
  folders?: boolean
  /** With folders: a file can be chosen as well. */
  filesToo?: boolean
  /** The template's sample (relative to the home): the picker opens where it is, with it selected. */
  sample?: string
  onChoose: (p: string) => void
  onClose: () => void
}) {
  const [place, setPlace] = useState(HOME_PLACE)
  const [dir, setDir] = useState(() => (sample ? parent(sample) : ''))
  const groupFolders = useStorage().data?.folders ?? []
  const folder = useHomeFolder(dir, true, place)
  const inHome = place === HOME_PLACE
  const rootLabel = inHome ? 'Home' : (groupFolders.find((f) => groupPlace(f.dir) === place)?.dir ?? place)
  const user = useMe().data?.id ?? ''
  const [showHidden, setShowHidden] = useState(() => readStored(hiddenKey(user)) === 'show')
  useEffect(() => {
    setShowHidden(readStored(hiddenKey(user)) === 'show')
  }, [user])
  const toggleHidden = (show: boolean) => {
    setShowHidden(show)
    writeStored(hiddenKey(user), show ? 'show' : 'hide')
  }
  const read = folder.data
  const shownFolder = read?.kind === 'listed' ? read.folder : undefined
  // A folder on the way to a sample that is not copied yet is simply empty.
  const towardsSample = inHome && !!sample && (dir === sample || dir === '' || sample.startsWith(`${dir}/`))
  const missing = towardsSample && folder.error instanceof ApiError && folder.error.status === 404
  const err = missing ? null : folder.error
  const reason = err instanceof ApiError && err.body.reason ? err.body : undefined
  const loading = !shownFolder && !err && !missing
  const slow = useAfter(loading, SLOW_MS, `${place}\n${dir}`)
  const opening = read?.kind === 'opening' ? read.reason : undefined

  let notice: ReactNode
  if (reason) {
    notice = <FailureCard title={reason.title} what={reason.what} todo={reason.todo} blame={reason.blame} who={reason.who} />
  } else if (loading && slow && opening) {
    notice = (
      <Banner tone="info" title={opening.title}>
        <p className="tight">{opening.what}</p>
        <p className="tight">{opening.todo}</p>
      </Banner>
    )
  }

  const listed = missing ? [] : (shownFolder?.entries ?? []).map((e) => toEntry(dir, e))
  const parts = dir ? dir.split('/') : []
  return (
    <div className="overlay open" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <FilePicker
        places={[
          { id: HOME_PLACE, label: 'Home' },
          ...(folders
            ? []
            : groupFolders.map((f) => ({ id: groupPlace(f.dir), label: f.dir, disabledReason: f.ready ? undefined : (f.reason?.title ?? '') }))),
        ]}
        activePlace={place}
        crumbs={[{ label: rootLabel, path: '' }, ...parts.map((p, i) => ({ label: p, path: parts.slice(0, i + 1).join('/') }))]}
        entries={sample && inHome ? withSample(dir, listed, sample, folders ? 'dir' : 'file') : listed}
        acceptExtensions={inHome ? extensions : []}
        acceptReason={inHome ? acceptReason : 'An app runs a file from your home folder'}
        {...extras}
        choose={folders ? (filesToo ? 'any' : 'folder') : 'file'}
        initialSelected={sample}
        showHidden={showHidden}
        onShowHiddenChange={toggleHidden}
        loading={loading}
        updating={shownFolder?.stale}
        notice={notice}
        note={shownFolder?.truncated ? `Showing the first ${shownFolder.entries.length} items` : undefined}
        error={err && !reason ? problemText(err) : undefined}
        onRetry={() => void folder.refetch()}
        onPlaceChange={(id) => {
          setPlace(id)
          setDir('')
        }}
        onNavigate={setDir}
        onChoose={onChoose}
        onCancel={onClose}
      />
    </div>
  )
}
