import { useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import type { PodRun } from '../../api'
import { CopyButton } from '../../components/CopyButton'
import { HomeLinkedText } from '../../components/HomePath'
import { sourceLink } from '../../lib/github'
import { useServerInfo } from '../../lib/queries'
import type { CodeToken } from '../files/fileSyntax'

/** The launcher pods run commands through, and where it lives in the repository. */
const POD_USER = '/opt/javo/pod-user.sh'
const POD_USER_SOURCE = 'pod/files/pod-user.sh'

/** The text with the launcher's path, wherever it appears, as a link to its source, and home paths as links to the Files page. */
function linkLauncher(text: string, href: string, style?: CSSProperties, home = true): ReactNode[] {
  return text.split(POD_USER).flatMap((piece, i) => [
    ...(i > 0
      ? [
          <a key={`l${i}`} href={href} target="_blank" rel="noopener noreferrer" style={style} title="pod-user.sh on GitHub">
            {POD_USER}
          </a>,
        ]
      : []),
    ...(piece ? [home ? <HomeLinkedText key={`t${i}`} text={piece} style={style} /> : <span key={`t${i}`} style={style}>{piece}</span>] : []),
  ])
}

/** The command line a pod runs, shell-coloured once the grammar loads (plain until then), wrapped, with a copy icon. */
export function CommandLine({ line, label = 'Copy command', linkHome = true }: { line: string; label?: string; linkHome?: boolean }) {
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
  const launcher = sourceLink(useServerInfo().data, POD_USER_SOURCE)
  return (
    <div className="codeblock cmdline">
      <code>
        {parts
          ? parts.map((t, i) => (
              <span key={i}>{linkLauncher(t.content, launcher, { color: t.color, fontStyle: t.italic ? 'italic' : undefined }, linkHome)}</span>
            ))
          : linkLauncher(line, launcher, undefined, linkHome)}
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
      {line ? <CommandLine line={line} linkHome={false} /> : <p className="muted small tight">The image&apos;s own command</p>}
    </div>
  )
}
