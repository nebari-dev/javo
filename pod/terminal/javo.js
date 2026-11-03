// javo's additions to ttyd's terminal page (page.mjs puts this after ttyd's
// script and xterm.js's clipboard addon). ttyd creates the terminal once it
// has fetched its token, assigns it to window.term, then opens it.
;(() => {
  const isMac = /Mac|iPhone|iPad/.test(navigator.platform)

  function setUp(term) {
    // Fira Code, set before xterm.js first measures its cells: changing it
    // later resizes the terminal under text already drawn.
    term.options.fontFamily = '"Fira Code", monospace'
    term.options.fontSize = 14

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
