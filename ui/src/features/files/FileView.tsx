import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Pencil, Save } from 'lucide-react'
import { Fragment, useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { rawFileUrl } from '../../lib/files'
import { ApiError, problemText } from '../../lib/http'
import { readHomeFile, saveHomeFile, type FileRead } from './fileApi'
import { fileKind, languageOf, type Language } from './fileKind'
import type { CodeToken } from './fileSyntax'
import type { EditorHandle } from './editor'

const fileKey = (path: string) => ['home-file', path] as const

/** Home, then each folder of path's parent, as links to the folder in the file browser. */
export function folderCrumbs(dir: string): { label: string; to: string }[] {
  const parts = dir ? dir.split('/') : []
  return [
    { label: 'Home', to: '/files' },
    ...parts.map((p, i) => ({ label: p, to: `/files?path=${encodeURIComponent(parts.slice(0, i + 1).join('/'))}` })),
  ]
}

/** Asks before leaving the page (reload, close, or a link) while there are unsaved changes. */
function useUnsavedGuard(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return
    const unload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    const click = (e: MouseEvent) => {
      const a = e.target instanceof Element ? e.target.closest('a[href]') : null
      if (a && !a.hasAttribute('download') && !window.confirm('Leave without saving your changes?')) {
        e.preventDefault()
        e.stopPropagation()
      }
    }
    window.addEventListener('beforeunload', unload)
    document.addEventListener('click', click, true)
    return () => {
      window.removeEventListener('beforeunload', unload)
      document.removeEventListener('click', click, true)
    }
  }, [dirty])
}

/** The text with line numbers; coloured once its grammar has loaded (plain until then). */
function CodeView({ name, text, language }: { name: string; text: string; language: Language }) {
  const [tokens, setTokens] = useState<{ text: string; lines: CodeToken[][] } | null>(null)
  useEffect(() => {
    let live = true
    void import('./fileSyntax')
      .then((m) => m.tokenize(text, language))
      .then((lines) => live && lines && setTokens({ text, lines }))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [text, language])
  const plain = text.replace(/\n$/, '').split('\n')
  const lines = tokens?.text === text ? tokens.lines : null
  return (
    <section className="fv-code" role="region" aria-label={`Contents of ${name}`} tabIndex={0}>
      {plain.map((line, i) => (
        <div key={i} className="fv-line">
          <span className="fv-ln" aria-hidden="true">
            {i + 1}
          </span>
          <span className="fv-text">
            {lines?.[i]
              ? lines[i].map((t, j) => (
                  <span key={j} style={{ color: t.color, fontStyle: t.italic ? 'italic' : undefined }}>
                    {t.content}
                  </span>
                ))
              : line}
            {'\n'}
          </span>
        </div>
      ))}
    </section>
  )
}

/** CodeMirror, loaded on first use, in a box that holds its shadow root. */
function Editor(p: { doc: string; language: Language; onChange: (dirty: boolean) => void; onSave: () => void; handle: (h: EditorHandle | null) => void }) {
  const box = useRef<HTMLDivElement>(null)
  const latest = useRef(p)
  latest.current = p
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let h: EditorHandle | null = null
    let live = true
    void import('./editor')
      .then((m) =>
        m.createEditor(box.current!, {
          doc: p.doc,
          language: p.language,
          onChange: (d) => latest.current.onChange(d),
          onSave: () => latest.current.onSave(),
        }),
      )
      .then((made) => {
        if (!live) return made.destroy()
        h = made
        latest.current.handle(made)
        made.focus()
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
      h?.destroy()
      latest.current.handle(null)
    }
    // The editor is made once per document; later edits live in it.
  }, [p.doc, p.language])
  if (failed) return <Banner tone="err" title="The editor could not be loaded. Reload the page and try again." />
  return <div className="fv-editor" ref={box} />
}

function TextFile({ path, name, read }: { path: string; name: string; read: Extract<FileRead, { kind: 'text' }> }) {
  const qc = useQueryClient()
  const language = languageOf(name)
  const [editing, setEditing] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [conflict, setConflict] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const editor = useRef<EditorHandle | null>(null)
  useUnsavedGuard(editing && dirty)

  const stop = () => {
    setEditing(false)
    setDirty(false)
    setConflict(null)
    setError(null)
  }
  const cancel = () => {
    if (dirty && !window.confirm('Throw away your changes?')) return
    stop()
  }
  const save = async () => {
    const h = editor.current
    if (!h || saving) return
    const text = h.text()
    setSaving(true)
    setError(null)
    try {
      const out = await saveHomeFile(path, text, read.etag)
      const next: FileRead = { kind: 'text', text, etag: out.etag }
      qc.setQueryData(fileKey(path), next)
      stop()
      setSaved(true)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) setConflict(problemText(err))
      else setError(problemText(err))
    } finally {
      setSaving(false)
    }
  }
  const reload = async () => {
    if (!window.confirm('Load the file as it is now? Your changes here are thrown away.')) return
    stop()
    // Dropped, not just marked stale, so the old text is not shown while the new one loads.
    await qc.resetQueries({ queryKey: fileKey(path) })
  }

  return (
    <>
      <div className="fv-bar">
        {editing ? (
          <>
            <span className="muted small">{dirty ? 'Unsaved changes' : 'No changes yet'}</span>
            <span className="fv-actions">
              <Button onClick={cancel} disabled={saving}>
                Cancel
              </Button>
              <Button variant="primary" icon={Save} onClick={() => void save()} disabled={saving}>
                {saving ? 'Saving' : 'Save'}
              </Button>
            </span>
          </>
        ) : (
          <>
            <span className="muted small" role="status">
              {saved ? 'Saved' : ''}
            </span>
            <span className="fv-actions">
              <a className="btn" href={rawFileUrl(path)} download={name}>
                <Download size={16} aria-hidden="true" />
                Download
              </a>
              <Button
                variant="primary"
                icon={Pencil}
                onClick={() => {
                  setSaved(false)
                  setEditing(true)
                }}
              >
                Edit
              </Button>
            </span>
          </>
        )}
      </div>
      {conflict && (
        <Banner
          tone="warn"
          title={conflict}
          actions={
            <Button size="sm" onClick={() => void reload()}>
              Reload
            </Button>
          }
        />
      )}
      {error && <Banner tone="err" title={error} />}
      {editing ? (
        <Editor doc={read.text} language={language} onChange={setDirty} onSave={() => void save()} handle={(h) => (editor.current = h)} />
      ) : (
        <CodeView name={name} text={read.text} language={language} />
      )}
    </>
  )
}

function DownloadOnly({ path, name, why }: { path: string; name: string; why: string }) {
  return (
    <Banner
      tone="info"
      title={why}
      actions={
        <a className="btn sm" href={rawFileUrl(path)} download={name}>
          <Download size={16} aria-hidden="true" />
          Download
        </a>
      }
    />
  )
}

function DownloadBar({ path, name }: { path: string; name: string }) {
  return (
    <div className="fv-bar">
      <span className="fv-actions">
        <a className="btn" href={rawFileUrl(path)} download={name}>
          <Download size={16} aria-hidden="true" />
          Download
        </a>
      </span>
    </div>
  )
}

function formatBytes(n: number): string {
  return n < 1 << 20 ? `${Math.ceil(n / 1024)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`
}

/** One file of the home: text shown (and edited) here, pictures and PDFs in the browser's own viewer, anything else as a download. */
export function FileView() {
  const [params] = useSearchParams()
  const path = params.get('path') ?? ''
  const name = path.slice(path.lastIndexOf('/') + 1)
  const dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
  const kind = fileKind(name)
  const [broken, setBroken] = useState(false)
  const read = useQuery({
    queryKey: fileKey(path),
    queryFn: () => readHomeFile(path),
    enabled: kind === 'text' && path !== '',
    staleTime: Infinity,
    // The home folder is opening: ask again until it is.
    retry: (n, err) => err instanceof ApiError && err.status === 503 && n < 30,
    retryDelay: 1000,
  })

  let body
  if (kind === 'image') {
    body = broken ? (
      <DownloadOnly path={path} name={name} why="This picture could not be shown." />
    ) : (
      <>
        <DownloadBar path={path} name={name} />
        <div className="fv-media">
          <img src={rawFileUrl(path)} alt={name} onError={() => setBroken(true)} />
        </div>
      </>
    )
  } else if (kind === 'pdf') {
    body = (
      <>
        <DownloadBar path={path} name={name} />
        <iframe className="fv-pdf" src={rawFileUrl(path)} title={name} />
      </>
    )
  } else if (read.error) {
    body = <Banner tone="err" title={problemText(read.error)} />
  } else if (!read.data) {
    body = <p className="muted">Opening {name}</p>
  } else if (read.data.kind === 'binary') {
    body = <DownloadOnly path={path} name={name} why="This file is not text, so it cannot be shown here." />
  } else if (read.data.kind === 'large') {
    body = <DownloadOnly path={path} name={name} why={`This file is too large to show here (${formatBytes(read.data.size)}; up to 2 MB is shown).`} />
  } else {
    body = <TextFile path={path} name={name} read={read.data} />
  }

  return (
    <div className="fv">
      <nav className="crumbs" aria-label="Breadcrumbs">
        {folderCrumbs(dir).map((c, i) => (
          <Fragment key={c.to}>
            {i > 0 && ' / '}
            <Link to={c.to}>{c.label}</Link>
          </Fragment>
        ))}
      </nav>
      <h1 className="fv-name">{name}</h1>
      {body}
    </div>
  )
}
