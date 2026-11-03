import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { heldSteps, idlePolicy, me, stoppedByAdmin, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

function setup(app: ReturnType<typeof ws>) {
  mockApi({
    'GET /api/me': { body: me },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/idle-stop': { body: idlePolicy },
    'GET /api/shared': { body: { items: [] } },
    'GET /api/workspaces/plots': { body: app },
    'GET /api/workspaces': { body: { items: [app] } },
  })
}

describe('Apps an admin stopped or a restore holds', () => {
  it('says an admin stopped the app, on its page and its card', async () => {
    setup(ws('plots', 'Plots', 'Stopped', stoppedByAdmin()))
    renderApp('/apps/plots')
    expect(await screen.findByText('Stopped by an admin')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Start' })).toBeTruthy()
  })

  it('says a restored app waits for an admin, with no Start or Stop', async () => {
    setup(ws('plots', 'Plots', 'Pending', heldSteps()))
    renderApp('/apps/plots')
    expect(await screen.findByText('Restored, waiting for an admin')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })

  it('shows the wait on the card too', async () => {
    setup(ws('plots', 'Plots', 'Pending', heldSteps()))
    renderApp('/apps')
    expect(await screen.findByText('Restored, waiting for an admin')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Stop/ })).toBeNull()
  })
})
