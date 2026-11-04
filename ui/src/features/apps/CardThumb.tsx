import { useState } from 'react'
import type { Thumbnail } from '../../api'
import { KindLogo } from '../../components/KindLogo'

/** The picture's URL until it fails to load, then undefined. */
function usePicture(thumbnail?: Thumbnail): [string | undefined, () => void] {
  const [broken, setBroken] = useState<string>()
  const url = thumbnail?.url && thumbnail.url !== broken ? thumbnail.url : undefined
  return [url, () => setBroken(url)]
}

/**
 * The large picture frame on the app page's "Card picture" section, present
 * only while the install has thumbnails on (the app view then carries
 * `thumbnail`). It shows the owner's picture, or the app kind's logo when
 * there is none or it fails to load.
 */
export function CardThumb({ kind, thumbnail }: { kind: string; thumbnail?: Thumbnail }) {
  const [url, fail] = usePicture(thumbnail)
  if (!thumbnail) return null
  return (
    <div className="thumb-preview" aria-hidden="true">
      {url ? <img src={url} alt="" loading="lazy" decoding="async" onError={fail} /> : <KindLogo kind={kind} big />}
    </div>
  )
}

/**
 * The mark at the start of an app card: the owner's picture as a small
 * frame when there is one that loads, else the kind's logo. Both are the
 * same height, so every card keeps one size.
 */
export function CardMark({ kind, thumbnail }: { kind: string; thumbnail?: Thumbnail }) {
  const [url, fail] = usePicture(thumbnail)
  if (!url) return <KindLogo kind={kind} />
  return (
    <div className="acard-thumb" aria-hidden="true">
      <img src={url} alt="" loading="lazy" decoding="async" onError={fail} />
    </div>
  )
}
