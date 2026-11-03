import type { SharedApp } from '../../api'
import { useTemplateName } from '../../lib/queries'
import { appLink, pillStatus } from './status'
import { CardThumb } from './CardThumb'
import { KindLogo } from '../../components/KindLogo'
import { Pill } from '../../components/Pill'

/** An app another person shared; it opens the app itself, since viewers have no management page. */
export function SharedCard({ app }: { app: SharedApp }) {
  const template = useTemplateName(app.template)
  const href = app.phase === 'Running' ? appLink(app.url) : undefined
  const body = (
    <>
      <CardThumb kind={app.kind} thumbnail={app.thumbnail} />
      <div className="row">
        <KindLogo kind={app.kind} />
        <div className="grow">
          <h3>{app.displayName}</h3>
          <div className="faint small">
            {template} · <span>from {app.owner.displayName}</span>
          </div>
        </div>
        <Pill status={pillStatus(app.phase)} />
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
