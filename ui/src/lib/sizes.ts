import type { Amount, CustomAllowance, Size } from '../api'

const GiB = 2 ** 30
const MiB = 2 ** 20

/** A number without trailing zeros, at most two decimals: 0.5, 1, 1.25. */
function amount(n: number): string {
  return String(Number(n.toFixed(2)))
}

/** "1 CPU · 2 GB": a size's cores and memory (GB and MB of 1024, as Kubernetes Gi and Mi). */
export function sizeSpecs(s: Pick<Size, 'cpu' | 'memory'>): string {
  const mem = s.memory >= GiB ? `${amount(s.memory / GiB)} GB` : `${amount(s.memory / MiB)} MB`
  return `${amount(s.cpu)} CPU · ${mem}`
}

/** "1 GPU", "2 GPUs". */
export function gpuCount(n: number): string {
  return n === 1 ? '1 GPU' : `${n} GPUs`
}

/** "Small · 1 CPU · 2 GB", "Training · 4 CPU · 16 GB · 1 GPU". */
export function sizeLabel(s: Size): string {
  return `${s.displayName} · ${sizeSpecs(s)}${s.gpu ? ` · ${gpuCount(s.gpu)}` : ''}`
}

/** What an app runs at: a size from the list, or a custom amount. */
export type SizeChoice = { size: string } | { resources: Amount }

/** "Custom · 2.5 CPU · 6 GB". */
export function customLabel(a: Amount): string {
  return `Custom · ${sizeSpecs(a)}`
}

/** The API's words for what is wrong with each part of a custom amount, if anything. */
export function amountProblems(c: CustomAllowance, a: Amount): { cpu?: string; memory?: string } {
  const check = (v: number, min: number, max: number) =>
    !Number.isFinite(v) || v < min ? c.minimum : max > 0 && v > max ? c.limit : undefined
  return { cpu: check(a.cpu, c.minCPU, c.maxCPU), memory: check(a.memory, c.minMemory, c.maxMemory) }
}

/** Whether choice can be sent as it is: a size, or a custom amount within the allowance. */
export function choiceReady(c: CustomAllowance | undefined, choice: SizeChoice): boolean {
  if (!('resources' in choice)) return true
  if (!c?.allowed) return false
  const p = amountProblems(c, choice.resources)
  return !p.cpu && !p.memory
}

/** The same choice: the same size, or the same amount. */
export function sameChoice(a: SizeChoice, b: SizeChoice): boolean {
  if ('size' in a && 'size' in b) return a.size === b.size
  if ('resources' in a && 'resources' in b) return a.resources.cpu === b.resources.cpu && a.resources.memory === b.resources.memory
  return false
}
