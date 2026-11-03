import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { SearchSelect } from './SearchSelect'

function Harness() {
  const [v, setV] = useState('Europe/London')
  return <SearchSelect label="Time zone" options={['America/New_York', 'Asia/Tokyo', 'Europe/London']} value={v} onChange={setV} />
}

describe('SearchSelect', () => {
  it('narrows the list as you type and picks an option', async () => {
    render(<Harness />)
    const box = screen.getByRole('combobox', { name: 'Time zone' })
    // A click then typing, as a person does it, searches instead of appending.
    await userEvent.click(box)
    await userEvent.keyboard('new york')
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['America/New_York'])
    await userEvent.click(screen.getByRole('option', { name: 'America/New_York' }))
    expect((box as HTMLInputElement).value).toBe('America/New_York')
  })
})
