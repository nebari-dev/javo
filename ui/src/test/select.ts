import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/** Opens a dropdown (components/Select) and picks the option with that name. */
export async function choose(trigger: HTMLElement, option: string | RegExp) {
  await userEvent.click(trigger)
  await userEvent.click(await screen.findByRole('option', { name: option }))
}

/** The names of a dropdown's options, read by opening it and closing it again. */
export async function optionsOf(trigger: HTMLElement) {
  await userEvent.click(trigger)
  const names = (await screen.findAllByRole('option')).map((o) => o.textContent)
  await userEvent.keyboard('{Escape}')
  return names
}
