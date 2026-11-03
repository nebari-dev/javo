import { describe, expect, it } from 'vitest'
import type { CustomAllowance, Size } from '../api'
import { startingChoice } from './sizes'

const GiB = 2 ** 30
const size = (name: string, cpu: number, memory: number, extra: Partial<Size> = {}): Size => ({
  name, displayName: name, cpu, memory, default: false, fits: true, ...extra,
})
const sizes = [size('small', 1, 2 * GiB, { default: true }), size('medium', 4, 8 * GiB), size('gpu', 8, 16 * GiB, { gpu: 1 })]
const allowance = (allowed: boolean, maxCPU = 4): CustomAllowance => ({
  allowed, maxCPU, maxMemory: 16 * GiB, minCPU: 0.1, minMemory: 0.25 * GiB, cpuStep: 0.1, memoryStep: 0.25 * GiB,
})

describe('startingChoice', () => {
  it('is the template default as a custom amount when the person may ask for it', () => {
    expect(startingChoice({ cpu: 0.25, memory: 0.5 * GiB }, sizes, allowance(true))).toEqual({ resources: { cpu: 0.25, memory: 0.5 * GiB } })
  })
  it('is the smallest size that holds the default otherwise, never a GPU size', () => {
    expect(startingChoice({ cpu: 2, memory: 4 * GiB }, sizes, allowance(false))).toEqual({ size: 'medium' })
    expect(startingChoice({ cpu: 6, memory: 4 * GiB }, sizes, allowance(true))).toEqual({ size: 'small' })
  })
  it('is the default size without a template default', () => {
    expect(startingChoice(undefined, sizes, allowance(true))).toEqual({ size: 'small' })
  })
})
