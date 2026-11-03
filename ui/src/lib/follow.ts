import { useEffect, useRef, useState } from 'react'

/** How long to wait before opening a followed response again: what it follows has not started yet, or its body ended while it still goes. */
const FOLLOW_RETRY_MS = 1000

/** Opens a response the server keeps open while it writes more; resolves to its body. */
type OpenBody = (signal: AbortSignal) => Promise<ReadableStream<Uint8Array>>

/**
 * The body of a followed response (a run's log, a startup step's log), read
 * as it arrives. Each open starts from the beginning again, since the server
 * replays what was written before. While `again` is true a body that ends or
 * fails is opened again after FOLLOW_RETRY_MS; otherwise it is read once.
 * Undefined until the first open answers. `key` names what is followed: a
 * new key starts over, and `open` may change without restarting.
 */
export function useFollowedBody(open: OpenBody, key: string, enabled: boolean, again: boolean): string | undefined {
  const [text, setText] = useState<string>()
  const opener = useRef(open)
  const repeat = useRef(again)
  useEffect(() => {
    opener.current = open
    repeat.current = again
  })
  useEffect(() => {
    if (!enabled) return
    const stop = new AbortController()
    const { signal } = stop
    void (async () => {
      while (!signal.aborted) {
        try {
          const body = await opener.current(signal)
          const reader = body.getReader()
          const decoder = new TextDecoder()
          let got = ''
          setText((t) => t ?? '')
          for (;;) {
            const { value, done } = await reader.read()
            if (done) break
            got += decoder.decode(value, { stream: true })
            setText(got)
          }
        } catch {
          // Not there yet (409), or the request was stopped: ask again below.
        }
        if (!repeat.current) return
        await new Promise((r) => setTimeout(r, FOLLOW_RETRY_MS))
      }
    })()
    return () => stop.abort()
  }, [key, enabled])
  return text
}
