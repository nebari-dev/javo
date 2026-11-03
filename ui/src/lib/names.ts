/** base, or base numbered from 2 when one of taken already has that name (case and spacing aside). */
export function firstFreeName(base: string, taken: string[]): string {
  const used = new Set(taken.map((n) => n.trim().toLowerCase()))
  if (!used.has(base.toLowerCase())) return base
  let n = 2
  while (used.has(`${base} ${n}`.toLowerCase())) n++
  return `${base} ${n}`
}
