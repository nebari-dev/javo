import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { FailureCard } from './FailureCard'

function setup() {
  return render(
    <FailureCard
      title="Your app ran out of memory"
      what="The app needed more memory than the Small size has."
      todo="Restart it with the Medium size."
      blame="Your code."
      who="You. If you need a bigger size, ask an admin."
      evidence={['OOMKilled exit code 137', 'memory limit 512Mi']}
    />,
  )
}

describe('FailureCard', () => {
  it('shows the title, what happened and what you can do without any click', () => {
    setup()
    expect(screen.getByText('Your app ran out of memory')).toBeTruthy()
    expect(screen.getByText('What happened')).toBeTruthy()
    expect(screen.getByText('The app needed more memory than the Small size has.')).toBeTruthy()
    expect(screen.getByText('What you can do')).toBeTruthy()
    expect(screen.getByText('Restart it with the Medium size.')).toBeTruthy()
  })

  it('hides blame, who can help and evidence behind a More control', () => {
    setup()
    expect(screen.queryByText('Your code.')).toBeNull()
    expect(screen.queryByText(/OOMKilled/)).toBeNull()
    const toggle = screen.getByRole('button', { name: 'Who can help, whose fault it is, and the evidence' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
  })

  it('reveals blame, who and evidence lines when opened', async () => {
    setup()
    await userEvent.click(screen.getByRole('button', { name: /Who can help/ }))
    expect(screen.getByText('Is it you or the platform?')).toBeTruthy()
    expect(screen.getByText('Your code.')).toBeTruthy()
    expect(screen.getByText('Who can help')).toBeTruthy()
    expect(screen.getByText(/OOMKilled exit code 137/)).toBeTruthy()
    expect(screen.getByText(/memory limit 512Mi/)).toBeTruthy()
  })

  it('omits the evidence block when there are no lines', async () => {
    render(<FailureCard title="t" what="w" todo="d" blame="b" who="h" />)
    await userEvent.click(screen.getByRole('button', { name: /Who can help/ }))
    expect(screen.queryByText('Evidence')).toBeNull()
  })

  it('is announced as an alert', () => {
    setup()
    expect(screen.getByRole('alert')).toBeTruthy()
  })
})
