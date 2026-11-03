import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { CodeBlock } from './CodeBlock'

describe('CodeBlock', () => {
  it('shows the text whole and copies it exactly', async () => {
    const user = userEvent.setup()
    const { container } = render(<CodeBlock text="bash /opt/javo/samples/jobs/print-time.sh" label="Copy command" />)
    expect(container.querySelector('code')?.textContent).toBe('bash /opt/javo/samples/jobs/print-time.sh')
    await user.click(screen.getByRole('button', { name: 'Copy command' }))
    expect(await navigator.clipboard.readText()).toBe('bash /opt/javo/samples/jobs/print-time.sh')
  })

  it('lets a path wrap only after a slash', () => {
    const { container } = render(<CodeBlock text="/opt/javo/x.sh" />)
    expect(container.querySelector('code')?.innerHTML).toBe('/<wbr>opt/<wbr>javo/<wbr>x.sh')
  })
})
