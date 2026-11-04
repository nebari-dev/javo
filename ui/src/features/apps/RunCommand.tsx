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
