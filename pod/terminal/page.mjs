// Builds the terminal page ttyd serves to the agent templates (ttyd --index):
// ttyd's own page, read from the ttyd binary, plus
//   - Fira Code (FiraCode-VF.woff2, inline: ttyd serves no other files),
//   - xterm.js's clipboard addon, so OSC 52 from tmux or herdr reaches the
//     browser clipboard, and javo.js, which loads it and sets the font.
//
//   node page.mjs <addon-clipboard.js> <out.html>
// Runs the ttyd on the PATH on a unix socket to read its page.
import { spawn } from 'node:child_process'
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs'
import { request } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

/** Returns ttydPage with the font face added to its head and the scripts after ttyd's own. */
export function buildPage(ttydPage, { font, scripts }) {
  for (const end of ['</head>', '</body>']) {
    if (ttydPage.split(end).length !== 2) throw new Error(`ttyd's page has no single ${end}; check page.mjs against this ttyd`)
  }
  for (const s of scripts) {
    if (/<\/script/i.test(s)) throw new Error('a script contains </script, which would end its element early')
  }
  const face =
    '<style>@font-face{font-family:"Fira Code";font-weight:300 700;font-display:block;' +
    `src:url(data:font/woff2;base64,${font.toString('base64')}) format("woff2")}</style>`
  const tags = scripts.map((s) => `<script>${s}</script>`).join('')
  return ttydPage.replace('</head>', () => `${face}</head>`).replace('</body>', () => `${tags}</body>`)
}

function readTtydPage() {
  const sock = join(mkdtempSync(join(tmpdir(), 'ttyd-')), 'ttyd.sock')
  const ttyd = spawn('ttyd', ['--interface', sock, 'true'], { stdio: 'ignore' })
  const get = (tries) =>
    new Promise((resolve, reject) => {
      request({ socketPath: sock, path: '/' }, (res) => {
        let body = ''
        res.setEncoding('utf8')
        res.on('data', (d) => (body += d))
        res.on('end', () => (res.statusCode === 200 ? resolve(body) : reject(new Error(`ttyd answered ${res.statusCode}`))))
      })
        .on('error', (e) => (tries > 0 ? setTimeout(() => get(tries - 1).then(resolve, reject), 100) : reject(e)))
        .end()
    })
  return get(50).finally(() => ttyd.kill())
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const [addon, out] = process.argv.slice(2)
  const page = buildPage(await readTtydPage(), {
    font: readFileSync(new URL('FiraCode-VF.woff2', import.meta.url)),
    scripts: [readFileSync(addon, 'utf8'), readFileSync(new URL('javo.js', import.meta.url), 'utf8')],
  })
  writeFileSync(out, page)
}
