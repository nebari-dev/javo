import { useEffect, useState } from 'react'
import type { PodRun } from '../../api'
import { CopyButton } from '../../components/CopyButton'
import type { CodeToken } from '../files/fileSyntax'

/** The command line a pod runs, shell-coloured once the grammar loads (plain until then), wrapped, with a copy icon. */
export function CommandLine({ line, label = 'Copy command' }: { line: string; label?: string }) {
  const [tokens, setTokens] = useState<{ line: string; parts: CodeToken[] } | null>(null)
  useEffect(() => {
    let live = true
    void import('../files/fileSyntax')
      .then((m) => m.tokenize(line, 'shell'))
      .then((lines) => live && lines && setTokens({ line, parts: lines.flat() }))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [line])
  const parts = tokens?.line === line ? tokens.parts : null
  return (
    <div className="codeblock cmdline">
      <code>
        {parts
          ? parts.map((t, i) => (
              <span key={i} style={{ color: t.color, fontStyle: t.italic ? 'italic' : undefined }}>
                {t.content}
              </span>
            ))
          : line}
      </code>
      <CopyButton label={label} text={() => line} />
    </div>
  )
}

/** "Started with": the command line the app's pod runs and, muted, its image; the first thing in its log. */
export function StartedWith({ run }: { run: PodRun }) {
  return (
    <div className="started-with" aria-label="Started with" role="group">
      <div className="started-with-head">
        <span className="started-with-label">Started with</span>
        <span className="muted mono started-with-image">{run.image}</span>
      </div>
      {run.line ? <CommandLine line={run.line} /> : <p className="muted small tight">The image&apos;s own command</p>}
    </div>
  )
}

/** What a new app of the template runs: its preview from the API with the file, command and image the person has given so far. */
export function TemplateRuns({ run, file, command, image }: { run: PodRun; file: string; command: string; image: string }) {
  const fill = (s: string) =>
    s
      .replaceAll('<your file>', file || '<your file>')
      .replaceAll('<your command>', command.trim() || '<your command>')
      .replaceAll('<your image>', image.trim() || '<your image>')
  const line = fill(run.line)
  return (
    <div className="started-with" role="group" aria-label="Runs">
      <div className="started-with-head">
        <span className="started-with-label">Runs</span>
        <span className="muted mono started-with-image">{fill(run.image)}</span>
      </div>
      {line ? <CommandLine line={line} /> : <p className="muted small tight">The image&apos;s own command</p>}
    </div>
  )
}
