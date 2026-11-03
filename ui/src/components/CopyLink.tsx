import { Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button } from './Button'
import { COPIED_MS } from './CopyButton'

/** A "Copy link" button that copies the link and briefly says "Copied". */
export function CopyLink({ link }: { link: string }) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), COPIED_MS)
    return () => clearTimeout(t)
  }, [copied])
  return (
    <Button size="sm" icon={copied ? Check : Copy} onClick={() => void navigator.clipboard?.writeText(link).then(() => setCopied(true))}>
      {copied ? 'Copied' : 'Copy link'}
    </Button>
  )
}
