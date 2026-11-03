import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GitBranch, House } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'
import { Segmented } from './Segmented'

const options = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta', disabled: true },
  { value: 'c', label: 'Gamma' },
]

describe('Segmented', () => {
  it('is a radio group with the chosen option checked and the only tab stop', () => {
    render(<Segmented label="Pick" value="a" options={options} onChange={() => {}} />)
    expect(screen.getByRole('radiogroup', { name: 'Pick' })).toBeTruthy()
    expect(screen.getByRole('radio', { name: 'Alpha' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('radio', { name: 'Alpha' }).getAttribute('tabindex')).toBe('0')
    expect(screen.getByRole('radio', { name: 'Gamma' }).getAttribute('tabindex')).toBe('-1')
  })

  it('arrow keys move focus past disabled options without choosing; Space or Enter chooses', async () => {
    const onChange = vi.fn()
    render(<Segmented label="Pick" value="a" options={options} onChange={onChange} />)
    screen.getByRole('radio', { name: 'Alpha' }).focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Gamma' }))
    expect(onChange).not.toHaveBeenCalled()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Alpha' }))
    await userEvent.keyboard('{ArrowLeft}')
    expect(onChange).not.toHaveBeenCalled()
    await userEvent.keyboard(' ')
    expect(onChange).toHaveBeenCalledExactlyOnceWith('c')
    await userEvent.keyboard('{Enter}')
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('does not choose a disabled option on click', async () => {
    const onChange = vi.fn()
    render(<Segmented label="Pick" value="a" options={options} onChange={onChange} />)
    await userEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('draws an option icon before its label, hidden from the accessible name', () => {
    render(
      <Segmented
        label="Where"
        value="home"
        options={[
          { value: 'home', label: 'Home folder', icon: House },
          { value: 'repo', label: 'Git repository', icon: GitBranch },
        ]}
        onChange={() => {}}
      />,
    )
    for (const name of ['Home folder', 'Git repository']) {
      const radio = screen.getByRole('radio', { name })
      const icon = radio.querySelector('svg')
      expect(icon?.getAttribute('aria-hidden')).toBe('true')
      expect(radio.firstElementChild).toBe(icon)
    }
  })

  it('keeps one tab stop when nothing is chosen', () => {
    render(<Segmented label="Pick" value="" options={options} onChange={() => {}} />)
    expect(screen.getByRole('radio', { name: 'Alpha' }).getAttribute('tabindex')).toBe('0')
  })
})
