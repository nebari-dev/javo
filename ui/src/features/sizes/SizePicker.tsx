import { Minus, Plus, Server } from 'lucide-react'
import { Section, useSectionHeading } from '../../components/Section'
import { useId, useState } from 'react'
import type { Amount, CustomAllowance, Size } from '../../api'
import { amountProblems, sizeLabel, sizeSpecs, type SizeChoice } from '../../lib/sizes'
import { Card } from '../../components/Card'
import { Folded } from '../../components/Folded'
import { IconButton } from '../../components/IconButton'
import { CUSTOM, SizeChoices } from './SizeChoices'

const GiB = 2 ** 30

interface SizePickerProps {
  sizes: Size[]
  /** The caller's custom amount allowance; no disclosure without one. */
  custom?: CustomAllowance
  value: SizeChoice
  onChange: (c: SizeChoice) => void
  /** Id of the element that names the group. */
  labelledBy: string
  describedBy?: string
  /** The API's error on the custom amount (request field resources). */
  customError?: string
}

type SizeCardProps = Omit<SizePickerProps, 'labelledBy' | 'describedBy' | 'customError'> & {
  errors: Record<string, string>
  /** Shows a size that can be picked as one row until Change; off, the card is always open. */
  foldable?: boolean
}

/**
 * The Size card of a form that makes an app or a job: the sizes, and the
 * API's error on the size field under them.
 */
export function SizeCard({ foldable = false, ...props }: SizeCardProps) {
  const { sizes, value, errors } = props
  const picked = 'size' in value ? sizes.find((s) => s.name === value.size) : undefined
  if (!foldable)
    return (
      <Section part="size" title="Size">
        <OpenSizeCard {...props} />
      </Section>
    )
  return (
    <Section part="size" title="Size">
    <Folded
      foldable={!!picked?.fits}
      attention={!!(errors.size || errors.resources)}
      icon={
        <span className="logo" aria-hidden="true">
          <Server size={20} />
        </span>
      }
      title="Size"
      value={picked ? sizeLabel(picked) : ''}
    >
      <OpenSizeCard {...props} />
    </Folded>
    </Section>
  )
}

function OpenSizeCard({ sizes, custom, value, onChange, errors }: Omit<SizeCardProps, 'foldable'>) {
  const ids = { err: useId() }
  const head = useSectionHeading()
  return (
    <Card className="pad">
      <SizePicker
        sizes={sizes}
        custom={custom}
        value={value}
        onChange={onChange}
        labelledBy={head ?? ''}
        describedBy={errors.size ? ids.err : undefined}
        customError={errors.resources}
      />
      {errors.size && (
        <span id={ids.err} className="errmsg">
          {errors.size}
        </span>
      )}
    </Card>
  )
}

/** A number as typed: no trailing zeros, at most two decimals. */
const text = (n: number) => String(Number(n.toFixed(2)))

/** n snapped to step, between min and max (max 0 is no limit). */
function snap(n: number, step: number, min: number, max: number) {
  const s = Math.round(n / step) * step
  return Math.max(min, max > 0 ? Math.min(s, Math.floor(max / step) * step) : s)
}

/**
 * The value one step from n in direction dir (1 up, -1 down), between min
 * and max (max 0 is no limit): off the step, the next step that way.
 * Empty (NaN), it starts at min.
 */
function stepFrom(n: number, dir: 1 | -1, step: number, min: number, max: number) {
  if (!Number.isFinite(n)) return min
  // Rounded so 0.30000000000000004 / 0.1 counts as on the step.
  const at = Number((n / step).toFixed(6))
  const next = (dir > 0 ? Math.floor(at) + 1 : Math.ceil(at) - 1) * step
  const top = max > 0 ? Math.floor(Number((max / step).toFixed(6))) * step : Infinity
  return Math.max(min, Math.min(next, top))
}

/**
 * The sizes as one radio group: the admin's sizes and, for people the admin
 * allows, a Custom card. Picking Custom opens CPU and memory fields below,
 * starting from the size picked before; picking a size again closes them.
 */
export function SizePicker({ sizes, custom, value, onChange, labelledBy, describedBy, customError }: SizePickerProps) {
  const isCustom = 'resources' in value
  // The size Custom starts from.
  const [lastSize, setLastSize] = useState(isCustom ? (sizes.find((s) => s.default)?.name ?? '') : value.size)
  const pick = (name: string) => {
    if (name !== CUSTOM) {
      setLastSize(name)
      return onChange({ size: name })
    }
    if (isCustom || !custom) return
    const from = sizes.find((s) => s.name === lastSize) ?? { cpu: custom.minCPU, memory: custom.minMemory }
    onChange({
      resources: {
        cpu: snap(from.cpu, custom.cpuStep, custom.minCPU, custom.maxCPU),
        memory: snap(from.memory, custom.memoryStep, custom.minMemory, custom.maxMemory),
      },
    })
  }
  return (
    <SizeChoices
      sizes={sizes}
      value={isCustom ? CUSTOM : value.size}
      onChange={pick}
      labelledBy={labelledBy}
      describedBy={describedBy}
      custom={custom?.allowed ? 'Set your own' : undefined}
    >
      {isCustom && custom?.allowed && (
        <CustomAmount allowance={custom} value={value.resources} onChange={(resources) => onChange({ resources })} error={customError} />
      )}
    </SizeChoices>
  )
}

function CustomAmount({ allowance: c, value, onChange, error }: { allowance: CustomAllowance; value: Amount; onChange: (a: Amount) => void; error?: string }) {
  // The fields keep what was typed; the amount follows as numbers.
  const [cpu, setCPU] = useState(text(value.cpu))
  const [mem, setMem] = useState(text(value.memory / GiB))
  const ids = { hint: useId(), err: useId() }
  const problems = amountProblems(c, value)
  const set = (nextCPU: string, nextMem: string) => {
    setCPU(nextCPU)
    setMem(nextMem)
    onChange({ cpu: nextCPU.trim() === '' ? NaN : Number(nextCPU), memory: nextMem.trim() === '' ? NaN : Math.round(Number(nextMem) * GiB) })
  }
  const memStep = c.memoryStep / GiB
  const fields = [
    { label: 'CPU', unit: 'CPU', less: 'Less CPU', more: 'More CPU', value: cpu, step: c.cpuStep, min: c.minCPU, max: c.maxCPU, problem: problems.cpu,
      change: (v: string) => set(v, mem), snap: () => Number.isFinite(value.cpu) && set(text(snap(value.cpu, c.cpuStep, 0, 0)), mem) },
    { label: 'Memory', unit: 'GB', less: 'Less memory', more: 'More memory', value: mem, step: memStep, min: c.minMemory / GiB, max: Math.floor(c.maxMemory / c.memoryStep) * memStep, problem: problems.memory,
      change: (v: string) => set(cpu, v), snap: () => Number.isFinite(value.memory) && set(cpu, text(snap(value.memory, c.memoryStep, 0, 0) / GiB)) },
  ]
  const shown = error ?? problems.cpu ?? problems.memory
  // Over the limit, the limit line itself turns into the error.
  const overLimit = shown !== undefined && shown === c.limit
  const other = shown !== undefined && !overLimit
  return (
    <div className="custom-amount" role="group" aria-label="Custom CPU and memory">
      <div className="custom-fields">
        {fields.map((f) => (
          <CustomField key={f.label} {...f} hintId={ids.hint} errId={other ? ids.err : undefined} />
        ))}
      </div>
      {other && (
        <span id={ids.err} className="errmsg" role="status">
          {shown}
        </span>
      )}
      {!shown && (
        <p className="tight">
          Runs at <strong>{sizeSpecs(value)}</strong>
        </p>
      )}
      <span id={ids.hint} className={overLimit ? 'errmsg' : 'muted small'} role={overLimit ? 'status' : undefined}>
        {c.limit}
      </span>
    </div>
  )
}

function CustomField(f: {
  label: string
  unit: string
  /** The minus and plus keys' names. */
  less: string
  more: string
  value: string
  step: number
  min: number
  max: number
  problem?: string
  change: (v: string) => void
  snap: () => void
  hintId: string
  errId?: string
}) {
  const id = useId()
  const n = f.value.trim() === '' ? NaN : Number(f.value)
  const step = (dir: 1 | -1) => f.change(text(stepFrom(n, dir, f.step, f.min, f.max)))
  return (
    <div className="field tight">
      <label htmlFor={id}>{f.label}</label>
      <span className="stepper">
        <IconButton icon={Minus} label={f.less} disabled={n <= f.min} onClick={() => step(-1)} />
        <span className="unit-input">
        <input
          id={id}
          type="number"
          inputMode="decimal"
          className={`input${f.problem ? ' err' : ''}`}
          value={f.value}
          step={f.step}
          min={f.min}
          max={f.max > 0 ? f.max : undefined}
          onChange={(e) => f.change(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
            e.preventDefault()
            step(e.key === 'ArrowUp' ? 1 : -1)
          }}
          onBlur={f.snap}
          aria-invalid={f.problem ? true : undefined}
          aria-describedby={f.problem && f.errId ? `${f.hintId} ${f.errId}` : f.hintId}
        />
        <span className="unit" aria-hidden="true">
          {f.unit}
        </span>
        </span>
        <IconButton icon={Plus} label={f.more} disabled={f.max > 0 && n >= f.max} onClick={() => step(1)} />
      </span>
    </div>
  )
}
