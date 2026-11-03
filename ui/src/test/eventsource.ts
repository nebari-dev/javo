/** A stand-in for the browser's EventSource (jsdom has none); tests drive it by hand. */
export class FakeEventSource {
  static instances: FakeEventSource[] = []
  readonly url: string
  readyState = 0
  closed = false
  onopen: ((e: Event) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  private listeners: Record<string, ((e: MessageEvent) => void)[]> = {}

  constructor(url: string | URL) {
    this.url = String(url)
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    ;(this.listeners[type] ??= []).push(fn)
  }

  removeEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.listeners[type] = (this.listeners[type] ?? []).filter((f) => f !== fn)
  }

  close() {
    this.closed = true
    this.readyState = 2
  }

  /** The server accepted the stream. */
  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  /** The server sent an event of type with JSON data. */
  emit(type: string, data: unknown) {
    for (const fn of this.listeners[type] ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }))
  }

  static latest(): FakeEventSource {
    const s = FakeEventSource.instances.at(-1)
    if (!s) throw new Error('no EventSource was opened')
    return s
  }
}
