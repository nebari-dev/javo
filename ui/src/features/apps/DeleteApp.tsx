import { Trash } from 'lucide-react'
import { useRef, useState } from 'react'
import type { Workspace } from '../../api'
import { Confirm } from '../../components/Confirm'
import { IconButton } from '../../components/IconButton'
import { useDeleteWorkspace } from '../../lib/queries'

/** The question before an app is deleted; onDeleted runs once it is gone. */
export function ConfirmDelete({ ws, onCancel, onDeleted }: { ws: Workspace; onCancel: () => void; onDeleted?: () => void }) {
  const del = useDeleteWorkspace(ws.name)
  return (
    <Confirm
      title={`Delete ${ws.displayName}?`}
      body="The app and its link go away. Files in your home folder are kept."
      confirm="Delete"
      icon={Trash}
      danger
      busy={del.isPending}
      error={del.isError ? del.error : undefined}
      onConfirm={() => del.mutate(undefined, { onSuccess: onDeleted })}
      onCancel={onCancel}
    />
  )
}

/**
 * The trash icon on an app's card and table row; it asks first. Not offered
 * on a pull request preview, which its repository manages.
 */
export function DeleteAppButton({ ws }: { ws: Workspace }) {
  const [confirming, setConfirming] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  if (ws.previewOf) return null
  return (
    <>
      <IconButton
        ref={btn}
        icon={Trash}
        label={`Delete ${ws.displayName}`}
        onClick={(e) => {
          // The card's link covers the card; the click must not open the app.
          e.stopPropagation()
          setConfirming(true)
        }}
      />
      {confirming && (
        <ConfirmDelete
          ws={ws}
          onDeleted={() => setConfirming(false)}
          onCancel={() => {
            setConfirming(false)
            btn.current?.focus()
          }}
        />
      )}
    </>
  )
}
