import type { CSSProperties, ReactNode } from 'react'
import { Link } from 'react-router'
import { HOME_DIR, homeFileLink } from '../lib/homeLinks'

/**
 * children as a link to path on the Files page when path is in the home,
 * as they are otherwise. Only for the viewer's own apps and jobs: the Files
 * page shows the viewer's home.
 */
export function HomePath({ path, children, style }: { path: string; children: ReactNode; style?: CSSProperties }) {
  const to = homeFileLink(path)
  return to ? (
    <Link to={to} style={style} title="Open in Files">
      {children}
    </Link>
  ) : (
    <span style={style}>{children}</span>
  )
}

// A home path inside a line: not part of a longer path or word, up to a space, quote or shell operator.
const IN_TEXT = new RegExp(`((?<![\\w./~-])${HOME_DIR.replace(/\//g, '\\/')}(?:\\/[^\\s"'\`()<>;|&]*)?(?![\\w.-]))`)

/** text with every home path in it linked to the Files page; render shapes each piece's text. */
export function HomeLinkedText({ text, style, render = (s) => s }: { text: string; style?: CSSProperties; render?: (s: string) => ReactNode }) {
  return text.split(IN_TEXT).map((piece, i) =>
    i % 2 === 1 ? (
      <HomePath key={i} path={piece} style={style}>
        {render(piece)}
      </HomePath>
    ) : (
      piece && (
        <span key={i} style={style}>
          {render(piece)}
        </span>
      )
    ),
  )
}
