// javo's additions to ttyd's terminal page (page.mjs puts this after ttyd's
// script and xterm.js's clipboard addon). ttyd creates the terminal once it
// has fetched its token, assigns it to window.term, then opens it.
;(() => {
  const isMac = /Mac|iPhone|iPad/.test(navigator.platform)

  // javo's colours (ui/src/styles/theme.css): the page's surface, text and
  // accent, and the log viewer's hues for the ANSI colours, each readable on
  // its background. The light theme's "white" is a grey, so text a program
  // prints in white, expecting a dark background, still shows.
  const THEMES = {
    light: {
      background: '#fcfcfd', foreground: '#1d2133', cursor: '#3d63dd', cursorAccent: '#fcfcfd',
      selectionBackground: '#3d63dd38', scrollbarSliderBackground: '#1d213326',
      scrollbarSliderHoverBackground: '#1d213340', scrollbarSliderActiveBackground: '#1d213359',
      black: '#1d2133', red: '#c71f2c', green: '#17773a', yellow: '#8c5d00',
      blue: '#0866d0', magenta: '#7a44d6', cyan: '#0a7286', white: '#5b6175',
      brightBlack: '#62687a', brightRed: '#c8283a', brightGreen: '#1f8a4c', brightYellow: '#a66400',
      brightBlue: '#3d63dd', brightMagenta: '#b3337f', brightCyan: '#08765a', brightWhite: '#8a90a3',
    },
    dark: {
      background: '#111319', foreground: '#e9ebf1', cursor: '#7c9bff', cursorAccent: '#111319',
      selectionBackground: '#7c9bff47', scrollbarSliderBackground: '#e9ebf126',
      scrollbarSliderHoverBackground: '#e9ebf140', scrollbarSliderActiveBackground: '#e9ebf159',
      black: '#353b48', red: '#ff7b72', green: '#3fb950', yellow: '#e3b341',
      blue: '#6cb6ff', magenta: '#d2a8ff', cyan: '#39c5cf', white: '#a2a8b8',
      brightBlack: '#737a8c', brightRed: '#ff6b6b', brightGreen: '#3ccf7a', brightYellow: '#f0b04a',
      brightBlue: '#7c9bff', brightMagenta: '#f778ba', brightCyan: '#56d4bc', brightWhite: '#e9ebf1',
    },
  }
  const dark = matchMedia('(prefers-color-scheme: dark)')
  const theme = () => THEMES[dark.matches ? 'dark' : 'light']

  // The page around the terminal takes its background, with a little more
  // room at the edges than ttyd's 5px.
  const style = document.createElement('style')
  style.textContent =
    'html,body{background:var(--javo-term-bg)}' +
    '#terminal-container .terminal{height:calc(100% - 20px);padding:10px 12px}' +
    // The request to copy text a program sent (askToCopy below).
    '.javo-copy{position:fixed;right:12px;bottom:12px;z-index:10;max-width:min(480px,calc(100% - 24px));' +
    'box-sizing:border-box;padding:12px;border:1px solid var(--javo-term-accent);border-radius:8px;' +
    'background:var(--javo-term-bg);color:var(--javo-term-fg);font:13px/1.4 system-ui,sans-serif}' +
    '.javo-copy pre{margin:8px 0;max-height:8em;overflow:auto;white-space:pre-wrap;word-break:break-all;' +
    'font:12px/1.4 "Fira Code",monospace}' +
    '.javo-copy button{margin-right:8px;padding:4px 12px;border:1px solid var(--javo-term-accent);' +
    'border-radius:6px;background:transparent;color:inherit;font:inherit;cursor:pointer}'
  document.head.append(style)
  const paint = () => {
    const t = theme()
    const root = document.documentElement.style
    root.setProperty('--javo-term-bg', t.background)
    root.setProperty('--javo-term-fg', t.foreground)
    root.setProperty('--javo-term-accent', t.cursor)
  }
  paint()

  // Asks before putting text a program sent on the clipboard: shows it (at
  // most 500 characters, control and direction characters made visible)
  // and copies it only on a click, the user gesture the browser and the
  // user both see. A newer request replaces the one shown.
  let request = null
  function askToCopy(term, text) {
    request?.remove()
    const shown = (text.length > 500 ? text.slice(0, 500) + '\u2026' : text).replace(
      /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g,
      '\ufffd',
    )
    const box = document.createElement('div')
    box.className = 'javo-copy'
    box.setAttribute('role', 'dialog')
    box.setAttribute('aria-label', 'Copy to clipboard')
    const note = document.createElement('div')
    note.textContent = `A program in this terminal wants to copy ${text.length} characters to your clipboard:`
    const pre = document.createElement('pre')
    pre.textContent = shown
    const close = () => {
      box.remove()
      if (request === box) request = null
      term.focus()
    }
    const button = (label, onClick) => {
      const b = document.createElement('button')
      b.type = 'button'
      b.textContent = label
      b.addEventListener('click', onClick)
      return b
    }
    box.append(
      note,
      pre,
      button('Copy', () => navigator.clipboard.writeText(text).finally(close)),
      button('Dismiss', close),
    )
    document.body.append(box)
    request = box
  }

  function setUp(term) {
    // Fira Code, set before xterm.js first measures its cells: changing it
    // later resizes the terminal under text already drawn. The variable font
    // has every weight: bold is semibold, which reads better than 700.
    term.options.fontFamily = '"Fira Code", monospace'
    term.options.fontSize = 14
    term.options.lineHeight = 1.2
    term.options.fontWeight = 400
    term.options.fontWeightBold = 600
    term.options.cursorStyle = 'bar'
    term.options.cursorBlink = true
    term.options.cursorInactiveStyle = 'outline'
    term.options.scrollback = 10000
    // Colours a program picks that are too faint on the background (white
    // text on the light theme) are shifted until they read.
    term.options.minimumContrastRatio = 4.5
    // The browser's light or dark mode, followed when it changes.
    term.options.theme = theme()
    dark.addEventListener('change', () => {
      paint()
      term.options.theme = theme()
    })

    // OSC 52 writes (tmux and herdr send a copied selection this way) go to
    // the browser clipboard at once only right after a mouse selection in
    // the terminal; any other is shown and copied on a click (osc52.js).
    // tmux sends an empty selection parameter, which means the clipboard
    // too, so every write is considered. Requests to read the clipboard are
    // answered with nothing: a program in the terminal never sees what the
    // user copied elsewhere.
    const guard = javoClipboardGuard({
      now: () => performance.now(),
      write: (text) => navigator.clipboard.writeText(text),
      ask: (text) => askToCopy(term, text),
    })
    // Capture phase, so the guard sees the mouse before xterm.js (or a
    // program that turned on mouse reporting) handles it.
    const inTerminal = (e) => term.element?.contains(e.target)
    document.addEventListener('mousedown', (e) => inTerminal(e) && guard.pointerDown(e.clientX, e.clientY), true)
    document.addEventListener('mouseup', (e) => inTerminal(e) && guard.pointerUp(e.clientX, e.clientY, e.detail), true)
    const provider = {
      readText: () => '',
      writeText: (_selection, text) => guard.offer(text),
    }
    term.loadAddon(new ClipboardAddon.ClipboardAddon(undefined, provider))

    // Ctrl+V pastes on Linux and Windows as Cmd+V does on a Mac: xterm.js
    // leaves the key to the browser, whose paste event needs no permission.
    if (!isMac) {
      term.attachCustomKeyEventHandler(
        (e) => !(e.type === 'keydown' && e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey && e.key === 'v'),
      )
    }

    // When the terminal gets narrower, xterm.js 5.4 (ttyd's) keeps each
    // line's cells beyond the new width, and its link detection reads them:
    // a wrapped link then opens with stale text inside. tmux and herdr
    // redraw the whole screen after a resize, so the buffer is cleared
    // instead and nothing stale is left.
    let cols = term.cols
    term.onResize((size) => {
      if (size.cols < cols) term.clear()
      cols = size.cols
    })
  }

  // Runs when ttyd assigns window.term, before it opens the terminal.
  let term
  Object.defineProperty(window, 'term', {
    configurable: true,
    get: () => term,
    set: (t) => {
      term = t
      setUp(t)
    },
  })

  // The font is inline in the page and loads at once; should the terminal
  // open first, xterm.js measured and drew glyphs with a fallback font, so
  // make it measure again, drop the glyphs it drew and fit the window.
  document.fonts.load('14px "Fira Code"').then(() => {
    if (!term?.element) return
    term.options.fontFamily = 'monospace'
    term.options.fontFamily = '"Fira Code", monospace'
    term.clearTextureAtlas()
    term.fit()
  })
})()
