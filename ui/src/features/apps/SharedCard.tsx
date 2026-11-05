import type { SharedApp } from '../../api'
import { useTemplateName } from '../../lib/queries'
import { appLink, pillStatus } from './status'
import { CardMark } from './CardThumb'
import { StatusIcon } from '../../components/Pill'

/** An app another person shared; it opens the app itself, since viewers have no management page. */
export function SharedCard({ app }: { app: SharedApp }) {
  const template = useTemplateName(app.template)
  const href = app.phase === 'Running' ? appLink(app.url) : undefined
  const body = (
    <>
      <div className="acard-head">
        <CardMark kind={app.kind} thumbnail={app.thumbnail} />
        <div className="grow">
          <h3>{app.displayName}</h3>
          <div className="acard-sub">
            {template} · <span>from {app.owner.displayName}</span>
          </div>
          <div className="acard-sub">{sharedHow(app.via)}</div>
        </div>
        <StatusIcon status={pillStatus(app.phase)} />
      </div>
    </>
  )
  return href ? (
    <a href={href} className="card acard" target="_blank" rel="noopener">
      {body}
    </a>
  ) : (
    <div className="card acard">{body}</div>
  )
}

/** How an app reaches the viewer, from the listing's `via`. */
export function sharedHow(via: string): string {
  if (via.startsWith('group:')) return `Shared with your group ${via.slice('group:'.length).split('/').pop()}`
  if (via === 'signed-in') return 'Shared with everyone signed in'
  if (via === 'public') return 'Anyone with the link'
  return 'Shared with you'
}
