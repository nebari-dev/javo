import { CircleStop } from 'lucide-react'
import { useRef, useState } from 'react'
import type { Workspace } from '../../api'
import type { useStartStop } from '../../lib/queries'
import { Button } from '../../components/Button'
import { Confirm } from '../../components/Confirm'

type StartStop = ReturnType<typeof useStartStop>

/**
 * The question before an app stops. On a failed app it reads "Stop retrying":
 * the platform keeps restarting a crashed app, and stopping ends that.
 */
export function StopConfirm({ ws, act, onDone, onCancel }: { ws: Workspace; act: StartStop; onDone: () => void; onCancel: () => void }) {
  const failed = ws.phase === 'Failed'
  return (
    <Confirm
      title={failed ? `Stop retrying ${ws.displayName}?` : `Stop ${ws.displayName}?`}
      body={
        failed
          ? 'The platform stops restarting it and frees its CPU and memory. Settings and files in your home folder are kept; start it again when you have fixed the problem.'
          : 'It shuts down and its link stops working until you start it again. Settings and files in your home folder are kept; anything not saved to disk is lost.'
      }
      confirm={failed ? 'Stop retrying' : 'Stop'}
      icon={CircleStop}
      busy={act.isPending}
      onConfirm={() => act.mutate('stop', { onSettled: onDone })}
      onCancel={onCancel}
    />
  )
}

/** Stop, after asking. */
export function StopButton({ ws, act, size }: { ws: Workspace; act: StartStop; size?: 'sm' }) {
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  return (
    <span className="stop-wrap">
      <Button ref={btn} size={size} icon={CircleStop} disabled={act.isPending} onClick={() => setAsking(true)}>
        <span className="stop-label">{ws.phase === 'Failed' ? 'Stop retrying' : 'Stop'}</span>
      </Button>
      {asking && (
        <StopConfirm
          ws={ws}
          act={act}
          onDone={() => setAsking(false)}
          onCancel={() => {
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </span>
  )
}
