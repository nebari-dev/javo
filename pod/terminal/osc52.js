// Which OSC 52 clipboard writes from the terminal reach the browser
// clipboard (page.mjs puts this before javo.js, which uses it). Any program
// in the terminal can print an OSC 52 sequence, so a command's output (a
// README, a git log, a prompt-injected agent) could put a command of its
// choosing on the clipboard, to be pasted later into another terminal.
// A write is made at once only when its text is exactly what the person has
// selected in the terminal right now (xterm.js's selection, which only the
// person's mouse makes): a program that writes then puts nothing on the
// clipboard the person did not pick. Any other write, however soon after a
// selection, is handed to ask, which shows the text and copies it only if
// the person clicks.
;(() => {
  globalThis.javoClipboardGuard = ({ selection, write, ask }) => ({
    offer(text) {
      const selected = selection()
      if (selected !== '' && text === selected) return write(text)
      ask(text)
    },
  })
})()
