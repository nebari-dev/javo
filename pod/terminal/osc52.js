// Which OSC 52 clipboard writes from the terminal reach the browser
// clipboard (page.mjs puts this before javo.js, which uses it). Any program
// in the terminal can print an OSC 52 sequence, so a command's output (a
// README, a git log, a prompt-injected agent) could put a command of its
// choosing on the clipboard, to be pasted later into another terminal.
// Writes are made at once only right after the user selected text with the
// mouse in the terminal (a drag, or a double or triple click): that is when
// tmux and herdr send their selection. Any other write is handed to ask,
// which shows the text and copies it only if the user clicks.
;(() => {
  const WINDOW_MS = 1000
  const DRAG_PX = 4

  globalThis.javoClipboardGuard = ({ now, write, ask }) => {
    let down = null
    let selectedAt = -Infinity
    return {
      pointerDown(x, y) {
        down = { x, y }
      },
      pointerUp(x, y, clicks) {
        const dragged = down !== null && Math.hypot(x - down.x, y - down.y) >= DRAG_PX
        down = null
        if (dragged || clicks >= 2) selectedAt = now()
      },
      offer(text) {
        if (now() - selectedAt <= WINDOW_MS) {
          selectedAt = -Infinity
          return write(text)
        }
        ask(text)
      },
    }
  }
})()
