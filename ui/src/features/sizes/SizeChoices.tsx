import { Gpu, Server, SlidersHorizontal } from 'lucide-react'
import { useId, type ReactNode } from 'react'
import type { Size } from '../../api'
import { gpuCount, sizeSpecs } from '../../lib/sizes'

/** The value of the Custom card in the size radio group. */
export const CUSTOM = 'custom'

interface SizeChoicesProps {
  sizes: Size[]
  /** The picked size's name, or CUSTOM. */
  value: string
  onChange: (name: string) => void
  /** Id of the element that names the group. */
  labelledBy: string
  describedBy?: string
  /** When set, a last card "Custom" with this line under its title. */
  custom?: string
  /** Shown right under the cards (the Custom amount's fields). */
  children?: ReactNode
}

/**
 * The admin-defined sizes as radio cards, like the template picker, and for
 * people the admin allows a Custom card in the same group. A size no
 * machine can hold is shown but cannot be picked: its card says so in a few
 * words, and the API's full reason is said once under the cards.
 */
export function SizeChoices({ sizes, value, onChange, labelledBy, describedBy, custom, children }: SizeChoicesProps) {
  const id = useId()
  const why = `${id}-why`
  const reason = sizes.find((s) => !s.fits)?.reason
  return (
    <div role="radiogroup" aria-labelledby={labelledBy} aria-describedby={describedBy} className="stack size-choices">
      <div className="grid choices">
        {sizes.map((s) => (
          <Card
            key={s.name}
            name={s.name}
            title={s.displayName}
            line={sizeSpecs(s)}
            gpu={s.gpu}
            checked={s.name === value}
            onPick={onChange}
            off={s.fits ? undefined : (s.reasonShort ?? s.reason)}
            describedBy={s.fits ? undefined : why}
          />
        ))}
        {custom !== undefined && <Card name={CUSTOM} title="Custom" line={custom} checked={value === CUSTOM} onPick={onChange} custom />}
      </div>
      {children}
      {reason && (
        <p id={why} className="tight muted small">
          {reason}
        </p>
      )}
    </div>
  )
}

function Card(p: {
  name: string
  title: string
  line: string
  /** GPUs at this size; unset or 0 shows none. */
  gpu?: number
  checked: boolean
  onPick: (name: string) => void
  /** The few words why it cannot be picked; set disables the card. */
  off?: string
  describedBy?: string
  custom?: boolean
}) {
  const Icon = p.custom ? SlidersHorizontal : Server
  return (
    <label className={`card choice${p.checked ? ' sel' : ''}${p.off ? ' off' : ''}`}>
      <input
        type="radio"
        name="size"
        className="sr-only"
        value={p.name}
        checked={p.checked}
        disabled={!!p.off}
        aria-describedby={p.describedBy}
        onChange={() => p.onPick(p.name)}
      />
      <span className="logo" aria-hidden="true">
        <Icon size={20} />
      </span>
      <span>
        <h3>{p.title}</h3>
        <span className="muted small block">{p.line}</span>
        {!!p.gpu && (
          <span className="muted small size-gpu">
            <Gpu size={14} aria-hidden="true" />
            {gpuCount(p.gpu)}
          </span>
        )}
        {p.off && <span className="choice-why">{p.off}</span>}
      </span>
    </label>
  )
}
