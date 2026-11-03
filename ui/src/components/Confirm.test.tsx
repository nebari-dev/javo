import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Square } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'
import { Confirm } from './Confirm'

describe('Confirm', () => {
  // A row or card can be clipped by its table or card; the dialog sits over
  // the page instead, so nothing around it can cut it off.
  it('opens as a modal over the page, outside the element that asked', () => {
    const { container } = render(
      <div className="atable">
        <Confirm title="Stop it?" body="Files are kept." confirm="Stop" icon={Square} busy={false} onConfirm={() => {}} onCancel={() => {}} />
      </div>,
    )
    const dialog = screen.getByRole('alertdialog', { name: 'Stop it?' })
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(container.contains(dialog)).toBe(false)
    expect(document.activeElement?.textContent).toBe('Cancel')
  })

  it('cancels on a click outside the dialog and on Escape', async () => {
    const onCancel = vi.fn()
    render(<Confirm title="Stop it?" body="Files are kept." confirm="Stop" icon={Square} busy={false} onConfirm={() => {}} onCancel={onCancel} />)
    await userEvent.click(screen.getByTestId('confirm-backdrop'))
    expect(onCancel).toHaveBeenCalledTimes(1)
    await userEvent.keyboard('{Escape}')
    expect(onCancel).toHaveBeenCalledTimes(2)
  })
})
