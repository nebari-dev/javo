import { render, screen } from '@testing-library/react'
import { optionsOf } from '../test/select'
import { describe, expect, it } from 'vitest'
import { TimePicker } from './TimePicker'

const hours = () => optionsOf(screen.getByLabelText('Hour'))

describe('TimePicker', () => {
  it('shows hours on a 24 hour clock where the locale uses it', async () => {
    render(<TimePicker label="At" hour={13} minute={5} onChange={() => {}} locale="en-GB" />)
    const shown = await hours()
    expect(shown.slice(0, 2)).toEqual(['00', '01'])
    expect(shown[13]).toBe('13')
  })

  it('and on a 12 hour clock where the locale uses that, with the same values', async () => {
    render(<TimePicker label="At" hour={13} minute={5} onChange={() => {}} locale="en-US" />)
    const shown = await hours()
    expect(shown[0]).toBe('12 AM')
    expect(shown[13]).toBe('01 PM')
    expect(screen.getByLabelText('Hour').textContent).toBe('01 PM')
  })
})
