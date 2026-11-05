import { Fragment } from 'react'
import { CopyButton } from './CopyButton'
import { HomeLinkedText, HomePath } from './HomePath'

/** The text with a line-break chance after each slash, so a path wraps between its parts, never inside a name. */
function breakable(text: string) {
  return text.split(/(?<=\/)/).map((part, i) => (
    <Fragment key={i}>
      {i > 0 && <wbr />}
      {part}
    </Fragment>
  ))
}

/**
 * A command or path on its own: monospace in a quiet frame, with a copy icon
 * in its corner. With path, the text links to that home path on the Files
 * page; with linkPaths, each home path in the text does.
 */
export function CodeBlock({ text, label = 'Copy', path, linkPaths }: { text: string; label?: string; path?: string; linkPaths?: boolean }) {
  return (
    <div className="codeblock">
      <code>{path ? <HomePath path={path}>{breakable(text)}</HomePath> : linkPaths ? <HomeLinkedText text={text} render={breakable} /> : breakable(text)}</code>
      <CopyButton label={label} text={() => text} />
    </div>
  )
}
