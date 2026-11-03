import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TimePicker } from './TimePicker'

const hours = () => within(screen.getByLabelText('Hour')).getAllByRole('option').map((o) => o.textContent)

describe('TimePicker', () => {
  it('shows hours on a 24 hour clock where the locale uses it', () => {
    render(<TimePicker label="At" hour={13} minute={5} onChange={() => {}} locale="en-GB" />)
    expect(hours().slice(0, 2)).toEqual(['00', '01'])
    expect(hours()[13]).toBe('13')
  })

  it('and on a 12 hour clock where the locale uses that, with the same values', () => {
    render(<TimePicker label="At" hour={13} minute={5} onChange={() => {}} locale="en-US" />)
    expect(hours()[0]).toBe('12 AM')
    expect(hours()[13]).toBe('01 PM')
    expect((screen.getByLabelText('Hour') as HTMLSelectElement).value).toBe('13')
  })
})
