import { useId, useLayoutEffect, useRef, useState } from 'react'
import { CopyButton } from '../../components/CopyButton'
import { MoreToggle } from '../../components/More'

/** An agent app's prompt as one line under its name, whole in the tooltip; nothing for an app without one. */
export function PromptLine({ prompt, className }: { prompt?: string; className: string }) {
  if (!prompt) return null
  return (
    <span className={`${className} prompt-line`} title={prompt}>
      {prompt}
    </span>
  )
}

/** The request an agent app was built on, quoted near the top of its page; a long one shows four lines until Show more. */
export function PromptBlock({ prompt }: { prompt?: string }) {
  const [open, setOpen] = useState(false)
  const [long, setLong] = useState(false)
  const text = useRef<HTMLQuoteElement>(null)
  const labelId = useId()
  const regionId = useId()
  useLayoutEffect(() => {
    const el = text.current
    if (!el || open) return
    const measure = () => setLong(el.scrollHeight > el.clientHeight + 1)
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [prompt, open])
  if (!prompt) return null
  return (
    <figure className="prompt-block" aria-labelledby={labelId}>
      <figcaption className="prompt-head">
        <span id={labelId}>Prompt</span>
        <CopyButton text={() => prompt} label="Copy prompt" />
      </figcaption>
      <blockquote ref={text} id={regionId} className={open ? 'prompt-text' : 'prompt-text clamped'}>
        {prompt}
      </blockquote>
      {(long || open) && (
        <MoreToggle open={open} onToggle={() => setOpen(!open)} controls={regionId}>
          {open ? 'Show less' : 'Show more'}
        </MoreToggle>
      )}
    </figure>
  )
}
