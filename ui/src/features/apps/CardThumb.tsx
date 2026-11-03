import { useState } from 'react'
import type { Thumbnail } from '../../api'
import { KindLogo } from '../../components/KindLogo'

/**
 * The picture band of an app card, present only while the install has
 * thumbnails on (the app view then carries `thumbnail`). It shows the
 * owner's picture, or the app kind's logo when there is none or it fails to
 * load, so every card keeps the same size.
 */
export function CardThumb({ kind, thumbnail, className = 'acard-thumb' }: { kind: string; thumbnail?: Thumbnail; className?: string }) {
  const [broken, setBroken] = useState<string>()
  if (!thumbnail) return null
  const url = thumbnail.url && thumbnail.url !== broken ? thumbnail.url : undefined
  return (
    <div className={className} aria-hidden="true">
      {url ? <img src={url} alt="" loading="lazy" decoding="async" onError={() => setBroken(url)} /> : <KindLogo kind={kind} big />}
    </div>
  )
}
