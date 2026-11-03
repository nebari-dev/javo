import { Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { IconButton } from './IconButton'

/** How long the button says "Copied". */
export const COPIED_MS = 1600

/** A clipboard icon that copies text and briefly turns into a check named "Copied". */
export function CopyButton({ text, label = 'Copy', disabled, className }: { text: () => string; label?: string; disabled?: boolean; className?: string }) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), COPIED_MS)
    return () => clearTimeout(t)
  }, [copied])
  return (
    <>
      <IconButton
        icon={copied ? Check : Copy}
        label={copied ? 'Copied' : label}
        className={className}
        disabled={disabled}
        onClick={() => void navigator.clipboard?.writeText(text()).then(() => setCopied(true))}
      />
      <span role="status" className="sr-only">
        {copied ? 'Copied' : ''}
      </span>
    </>
  )
}
