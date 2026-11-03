import { ImageUp, Trash2 } from 'lucide-react'
import { useId, useRef } from 'react'
import type { Workspace } from '../../api'
import { problemText } from '../../lib/http'
import { useRemoveThumbnail, useSetThumbnail } from '../../lib/queries'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { CardThumb } from './CardThumb'

/** The picture on the app's card: upload a screenshot, replace or remove it. Shown only while thumbnails are on. */
export function ThumbnailCard({ ws }: { ws: Workspace }) {
  const set = useSetThumbnail(ws.name)
  const remove = useRemoveThumbnail(ws.name)
  const input = useRef<HTMLInputElement>(null)
  const inputId = useId()
  if (!ws.thumbnail) return null
  const has = !!ws.thumbnail.url
  const busy = set.isPending || remove.isPending
  const error = set.isError ? set.error : remove.isError ? remove.error : null
  return (
    <Card className="pad">
      <div className="label">
        <ImageUp size={15} aria-hidden="true" />
        Card picture
      </div>
      <CardThumb kind={ws.kind} thumbnail={ws.thumbnail} className="acard-thumb thumb-preview" />
      <p className="tight muted small">
        A screenshot of the app, shown on its card to everyone who can open it. PNG or JPEG, up to 4 MB.
      </p>
      <input
        ref={input}
        id={inputId}
        className="sr-only"
        type="file"
        accept="image/png,image/jpeg"
        aria-label="Upload a picture"
        onChange={(e) => {
          const f = e.target.files?.[0]
          e.target.value = ''
          if (!f) return
          remove.reset()
          set.mutate(f)
        }}
      />
      <div className="row">
        <Button size="sm" icon={ImageUp} disabled={busy} onClick={() => input.current?.click()}>
          {has ? 'Replace' : 'Upload'}
        </Button>
        {has && (
          <Button
            size="sm"
            icon={Trash2}
            disabled={busy}
            onClick={() => {
              set.reset()
              remove.mutate()
            }}
          >
            Remove
          </Button>
        )}
      </div>
      {error && <span className="errmsg block">{problemText(error)}</span>}
    </Card>
  )
}
