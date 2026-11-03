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
    '#terminal-container .terminal{height:calc(100% - 20px);padding:10px 12px}'
  document.head.append(style)
  const paint = () => document.documentElement.style.setProperty('--javo-term-bg', theme().background)
  paint()

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
    // the browser clipboard. tmux sends an empty selection parameter, which
    // means the clipboard too, so every write is taken. Requests to read the
    // clipboard are answered with nothing: a program in the terminal never
    // sees what the user copied elsewhere.
    const provider = {
      readText: () => '',
      writeText: (_selection, text) => navigator.clipboard.writeText(text),
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
