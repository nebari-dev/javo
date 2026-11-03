import { ChevronRight, File, FilePlus, Folder } from 'lucide-react'
import { Fragment, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { EmptyState } from '../../components/EmptyState'
import { Switch } from '../../components/Switch'
import { problemText } from '../../lib/http'
import { useHomeFolder, useWarmHome } from '../../lib/queries'
import { saveHomeFile } from './fileApi'
import { folderCrumbs } from './FileView'

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)} ${units[i]}`
}

/** A form for a new, empty file in dir; it opens the file once made. */
function NewFile({ dir, onDone }: { dir: string; onDone: () => void }) {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n || n === '.' || n === '..' || n.includes('/')) {
      setError('Give the file a name without a slash.')
      return
    }
    setBusy(true)
    try {
      await saveHomeFile(join(dir, n), '', null)
      navigate(`/files/view?path=${encodeURIComponent(join(dir, n))}`)
    } catch (err) {
      setError(problemText(err))
      setBusy(false)
    }
  }
  return (
    <form className="fb-new" onSubmit={(e) => void submit(e)}>
      <input className="input mono" aria-label="File name" placeholder="notes.md" value={name} autoFocus onChange={(e) => setName(e.target.value)} />
      <Button type="submit" variant="primary" disabled={busy}>
        Create
      </Button>
      <Button onClick={onDone}>Cancel</Button>
      {error && <span className="fb-err">{error}</span>}
    </form>
  )
}

/** The home folder, one folder at a time: folders open here, files in the file view. */
export function FilesPage() {
  useWarmHome()
  const [params] = useSearchParams()
  const dir = params.get('path') ?? ''
  const folder = useHomeFolder(dir, true)
  const [showHidden, setShowHidden] = useState(false)
  const [creating, setCreating] = useState(false)
  const read = folder.data
  const listed = read?.kind === 'listed' ? read.folder : undefined
  const entries = (listed?.entries ?? []).filter((e) => showHidden || !e.name.startsWith('.'))
  const crumbs = folderCrumbs(dir)

  let body
  if (folder.error) {
    body = <Banner tone="err" title={problemText(folder.error)} actions={<Button size="sm" onClick={() => void folder.refetch()}>Retry</Button>} />
  } else if (!listed) {
    body = <p className="muted">{read?.kind === 'opening' ? read.reason.title : 'Opening this folder'}</p>
  } else if (entries.length === 0) {
    body = <EmptyState title="This folder is empty" body="Make a file here with New file, or save one from an app." />
  } else {
    body = (
      <div className="fb-list">
        {entries.map((e) => {
          const path = join(dir, e.name)
          const isDir = e.kind === 'folder'
          const to = isDir ? `/files?path=${encodeURIComponent(path)}` : `/files/view?path=${encodeURIComponent(path)}`
          return (
            <Link key={e.name} to={to} className="pk-row">
              <span className="fi">{isDir ? <Folder size={18} aria-hidden="true" /> : <File size={18} aria-hidden="true" />}</span>
              <span className={`nm${isDir ? ' dir' : ''}`}>{e.name}</span>
              <span className="md">{new Date(e.modified).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })}</span>
              <span className="sz">{isDir ? <ChevronRight size={14} aria-hidden="true" /> : formatSize(e.size)}</span>
            </Link>
          )
        })}
        {listed.truncated && <div className="pk-note faint">Showing the first {listed.entries.length} items</div>}
      </div>
    )
  }

  return (
    <>
      <div className="section-head page-head">
        <h1 className="flush">Files</h1>
        <div className="right">
          <Button variant="primary" icon={FilePlus} onClick={() => setCreating(true)}>
            New file
          </Button>
        </div>
      </div>
      <p className="sub">Your home folder. Open a file to read it, or to edit a text file.</p>
      <div className="fb-head">
        <nav className="crumbs" aria-label="Breadcrumbs">
          {crumbs.map((c, i) => (
            <Fragment key={c.to}>
              {i > 0 && ' / '}
              {i === crumbs.length - 1 ? <span aria-current="page">{c.label}</span> : <Link to={c.to}>{c.label}</Link>}
            </Fragment>
          ))}
        </nav>
        <Switch label="Show hidden files" checked={showHidden} onChange={setShowHidden} />
      </div>
      {creating && <NewFile dir={dir} onDone={() => setCreating(false)} />}
      {body}
    </>
  )
}
