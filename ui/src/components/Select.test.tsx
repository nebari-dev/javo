import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Select } from './Select'

function Kind({ onValueChange = () => {}, disabled }: { onValueChange?: (v: string) => void; disabled?: boolean }) {
  const [v, setV] = useState('')
  return (
    <>
      <label htmlFor="kind">From</label>
      <Select
        id="kind"
        value={v}
        disabled={disabled}
        onValueChange={(next) => {
          setV(next)
          onValueChange(next)
        }}
      >
        <option value="">Nothing</option>
        <option value="value">Value</option>
        <option value="secret">Secret</option>
      </Select>
    </>
  )
}

describe('Select', () => {
  it('is named by its label and shows the chosen option, an empty value too', () => {
    render(<Kind />)
    expect(screen.getByRole('combobox', { name: 'From' }).textContent).toBe('Nothing')
  })

  it('opens a list, marks the chosen option and reports the one picked', async () => {
    const picked = vi.fn()
    render(<Kind onValueChange={picked} />)
    const trigger = screen.getByRole('combobox', { name: 'From' })
    await userEvent.click(trigger)
    expect(screen.getByRole('option', { name: 'Nothing' }).getAttribute('aria-selected')).toBe('true')
    await userEvent.click(screen.getByRole('option', { name: 'Secret' }))
    expect(picked).toHaveBeenCalledWith('secret')
    expect(trigger.textContent).toBe('Secret')
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('picks with the keyboard and with typeahead', async () => {
    const picked = vi.fn()
    render(<Kind onValueChange={picked} />)
    screen.getByRole('combobox', { name: 'From' }).focus()
    await userEvent.keyboard('{Enter}')
    expect(screen.getByRole('listbox')).toBeTruthy()
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(picked).toHaveBeenLastCalledWith('value')
    await userEvent.keyboard('{Enter}')
    await userEvent.keyboard('s')
    await userEvent.keyboard('{Enter}')
    expect(picked).toHaveBeenLastCalledWith('secret')
  })

  it('does not open while disabled', async () => {
    render(<Kind disabled />)
    const trigger = screen.getByRole('combobox', { name: 'From' }) as HTMLButtonElement
    expect(trigger.disabled).toBe(true)
    await userEvent.click(trigger)
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('sends its value with the form it is in', async () => {
    const sent = vi.fn()
    function Form() {
      const [v, setV] = useState('b')
      return (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            sent(new FormData(e.currentTarget).get('pick'))
          }}
        >
          <Select name="pick" aria-label="Pick" value={v} onValueChange={setV}>
            <option value="a">A</option>
            <option value="b">B</option>
          </Select>
          <button type="submit">Send</button>
        </form>
      )
    }
    render(<Form />)
    await userEvent.click(screen.getByRole('combobox', { name: 'Pick' }))
    await userEvent.click(screen.getByRole('option', { name: 'A' }))
    await userEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(sent).toHaveBeenCalledWith('a')
  })
})
