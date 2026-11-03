import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { allReady, allStopped, idlePolicy, me, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const PIC = '/api/workspaces/plots/thumbnail?owner=u-1&v=abc123'

function cardOf(name: string): HTMLElement {
  return screen.getByRole('link', { name }).closest('.acard') as HTMLElement
}

function listing(items: unknown[], shared: unknown[] = []) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/shared': { body: { items: shared } },
    'GET /api/workspaces': { body: { items } },
  })
}

describe('App card pictures', () => {
  it('shows nothing new while thumbnails are off', async () => {
    listing([ws('plots', 'Plots', 'Running', allReady())])
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Plots' })
    expect(cardOf('Plots').querySelector('.acard-thumb')).toBeNull()
  })

  it('shows the picture, and the kind logo in the same frame when there is none', async () => {
    listing([
      ws('plots', 'Plots', 'Running', allReady(), { thumbnail: { url: PIC } }),
      ws('sales-app', 'Sales app', 'Stopped', allStopped(), { thumbnail: {} }),
    ])
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Plots' })
    const pic = cardOf('Plots').querySelector('.acard-thumb img')
    expect(pic?.getAttribute('src')).toBe(PIC)
    const blank = cardOf('Sales app').querySelector('.acard-thumb') as HTMLElement
    expect(blank).toBeTruthy()
    expect(blank.querySelector('img[src^="/api/"]')).toBeNull()
    expect(blank.querySelector('.logo')).toBeTruthy()
  })

  it('falls back to the kind logo when the picture does not load', async () => {
    listing([ws('plots', 'Plots', 'Running', allReady(), { thumbnail: { url: PIC } })])
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Plots' })
    fireEvent.error(cardOf('Plots').querySelector('.acard-thumb img') as HTMLImageElement)
    const frame = cardOf('Plots').querySelector('.acard-thumb') as HTMLElement
    expect(frame.querySelector('img[src^="/api/"]')).toBeNull()
    expect(frame.querySelector('.logo')).toBeTruthy()
  })

  it('shows the picture of an app someone shared', async () => {
    const shared = {
      name: 'report', displayName: 'Report', template: 'streamlit', kind: 'streamlit', phase: 'Running',
      url: '/u/bob/report/', via: 'user', owner: { id: 'u-2', displayName: 'Bob' },
      thumbnail: { url: '/api/workspaces/report/thumbnail?owner=u-2&v=d1' },
    }
    listing([], [shared])
    renderApp('/apps?show=shared')
    const card = (await screen.findByRole('link', { name: /Report/ })).closest('.acard') as HTMLElement
    expect(card.querySelector('.acard-thumb img')?.getAttribute('src')).toBe(shared.thumbnail.url)
  })
})

describe('App page picture', () => {
  const app = ws('plots', 'Plots', 'Running', allReady(), { thumbnail: {} })
  function page(over: Record<string, unknown> = {}) {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/idle-stop': { body: idlePolicy },
      'GET /api/workspaces/plots': { body: app },
      ...over,
    })
  }

  it('is not offered while thumbnails are off', async () => {
    page({ 'GET /api/workspaces/plots': { body: { ...app, thumbnail: undefined } } })
    renderApp('/apps/plots')
    await screen.findByRole('heading', { name: 'Plots' })
    expect(screen.queryByText('Card picture')).toBeNull()
  })

  it('uploads the chosen image as is and shows it', async () => {
    const api = page({ 'PUT /api/workspaces/plots/thumbnail': { body: { ...app, thumbnail: { url: PIC } } } })
    renderApp('/apps/plots')
    const section = (await screen.findByText('Card picture')).closest('.card') as HTMLElement
    const file = new File(['png-bytes'], 'shot.png', { type: 'image/png' })
    await userEvent.upload(within(section).getByLabelText('Upload a picture'), file)
    await waitFor(() => expect(api.count('PUT', '/api/workspaces/plots/thumbnail')).toBe(1))
    const put = api.calls.find((c) => c.method === 'PUT')
    expect(put?.contentType).toBe('image/png')
    expect(put?.body).toBe('png-bytes')
    await waitFor(() => expect(section.querySelector('img')?.getAttribute('src')).toBe(PIC))
  })

  it('removes the picture', async () => {
    const api = page({
      'GET /api/workspaces/plots': { body: { ...app, thumbnail: { url: PIC } } },
      'DELETE /api/workspaces/plots/thumbnail': { body: app },
    })
    renderApp('/apps/plots')
    const section = (await screen.findByText('Card picture')).closest('.card') as HTMLElement
    await userEvent.click(within(section).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(api.count('DELETE', '/api/workspaces/plots/thumbnail')).toBe(1))
    await waitFor(() => expect(within(section).queryByRole('button', { name: 'Remove' })).toBeNull())
  })

  it('says why an upload was refused', async () => {
    page({
      'PUT /api/workspaces/plots/thumbnail': {
        status: 422,
        body: { title: 'This is not a PNG or JPEG image. Upload a screenshot saved as PNG or JPEG.', status: 422 },
      },
    })
    renderApp('/apps/plots')
    const section = (await screen.findByText('Card picture')).closest('.card') as HTMLElement
    const input = within(section).getByLabelText('Upload a picture')
    // The picker offers PNG and JPEG only; the server checks the bytes anyway.
    expect(input.getAttribute('accept')).toBe('image/png,image/jpeg')
    fireEvent.change(input, { target: { files: [new File(['<svg/>'], 'x.png', { type: 'image/png' })] } })
    expect(await within(section).findByText(/not a PNG or JPEG/)).toBeTruthy()
  })
})
