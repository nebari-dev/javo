import { ChevronDown } from 'lucide-react'
import type { Ref, SelectHTMLAttributes } from 'react'

interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  ref?: Ref<HTMLSelectElement>
}

/**
 * A native select drawn like the text inputs: the browser's own arrow is
 * hidden and a line chevron sits inside the right padding, centred.
 */
export function Select({ className, ...rest }: SelectProps) {
  return (
    <span className="select">
      <select className={`input${className ? ` ${className}` : ''}`} {...rest} />
      <ChevronDown size={16} aria-hidden="true" className="select-chev" />
    </span>
  )
}
