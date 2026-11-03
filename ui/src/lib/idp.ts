import { useSyncExternalStore } from 'react'

/** Response header carrying the outage banner text, written by the gateway from its message catalog (see gateway.IdpUnreachableMessageHeader). */
const IDP_UNREACHABLE_HEADER = 'Javo-Idp-Unreachable-Message'

let message: string | null = null
const listeners = new Set<() => void>()

export function currentIdp(): string | null {
  return message
}

/** Records the outage text from a response; a response without the header means the outage is over. */
export function noteIdpHeader(headers: Headers): void {
  const next = headers.get(IDP_UNREACHABLE_HEADER) || null
  if (next === message) return
  message = next
  listeners.forEach((l) => l())
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** The outage banner text while the sign-in service cannot be asked, or null. */
export function useIdpOutage(): string | null {
  return useSyncExternalStore(subscribe, currentIdp)
}
