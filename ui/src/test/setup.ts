import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'
import { FakeEventSource } from './eventsource'

// jsdom has no EventSource; every test gets the fake.
// It also lacks ResizeObserver and scrollIntoView, which the command palette (cmdk) calls.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
}
Element.prototype.scrollIntoView ??= function () {}
// Radix Select (the dropdowns) asks about pointer capture, which jsdom lacks.
Element.prototype.hasPointerCapture ??= () => false
Element.prototype.releasePointerCapture ??= () => {}
Object.assign(globalThis, { EventSource: FakeEventSource })
import { noteIdpHeader } from '../lib/idp'
import { applyBrand, DEFAULT_BRAND } from '../lib/brand'

afterEach(() => {
  cleanup()
  FakeEventSource.instances = []
  noteIdpHeader(new Headers())
  applyBrand(DEFAULT_BRAND)
  try {
    localStorage.clear()
  } catch {
    // storage unavailable
  }
})
