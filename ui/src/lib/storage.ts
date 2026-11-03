// localStorage can throw (private windows, blocked site data), so every access is guarded.
export function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writeStored(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // not persisted; the UI still works for this session
  }
}
