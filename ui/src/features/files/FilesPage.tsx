import { FilePlus } from 'lucide-react'
import { Fragment, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Button } from '../../components/Button'
import { fileViewUrl } from '../../lib/files'
import { problemText } from '../../lib/http'
import { useWarmHome } from '../../lib/queries'
import { HomeTree, TreeTools, useShowHidden } from './HomeTree'
import { saveHomeFile } from './fileApi'
import { folderCrumbs } from './FileView'

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

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

/**
 * The home folder as a tree: folders open in place, files in the file view.
 * The folder last opened is where a new file goes, and the address names it.
 */
export function FilesPage() {
  useWarmHome()
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const dir = params.get('path') ?? ''
  const [query, setQuery] = useState('')
  const [showHidden, setShowHidden] = useShowHidden()
  const [creating, setCreating] = useState(false)
  const crumbs = folderCrumbs(dir)

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
        <span className="fb-tools">
          <TreeTools query={query} onQuery={setQuery} showHidden={showHidden} onShowHidden={setShowHidden} />
        </span>
      </div>
      {creating && <NewFile dir={dir} onDone={() => setCreating(false)} />}
      <div className="fb-list">
        <HomeTree
          label="Your home"
          query={query}
          showHidden={showHidden}
          meta
          openAt={dir ? [`${dir}/`] : []}
          reveal={dir || undefined}
          look={(e) => ({ selected: e.path === dir ? true : undefined, className: e.path === dir ? 'sel' : undefined })}
          onActivate={(e, toggle) => {
            if (e.kind === 'file') {
              navigate(fileViewUrl(e.path))
              return
            }
            toggle()
            navigate(`/files?path=${encodeURIComponent(e.path)}`, { replace: true })
          }}
        />
      </div>
    </>
  )
}
