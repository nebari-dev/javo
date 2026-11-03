import { Fragment } from 'react'
import { CopyButton } from './CopyButton'

/** The text with a line-break chance after each slash, so a path wraps between its parts, never inside a name. */
function breakable(text: string) {
  return text.split(/(?<=\/)/).map((part, i) => (
    <Fragment key={i}>
      {i > 0 && <wbr />}
      {part}
    </Fragment>
  ))
}

/** A command or path on its own: monospace in a quiet frame, with a copy icon in its corner. */
export function CodeBlock({ text, label = 'Copy' }: { text: string; label?: string }) {
  return (
    <div className="codeblock">
      <code>{breakable(text)}</code>
      <CopyButton label={label} text={() => text} />
    </div>
  )
}
