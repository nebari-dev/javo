#!/usr/bin/env node
// javo-api calls the javo API as the app's owner, with $JAVO_URL and $JAVO_TOKEN.
//   javo-api /api/me
//   javo-api POST /api/workspaces '{"displayName":"Demo","template":"custom","command":"python -m http.server {{.Port}}"}'
// It prints the answer as indented JSON, exits 1 on an error answer and 2 on a
// usage mistake. The token is sent, never printed.
import { pathToFileURL } from 'node:url'

const METHODS = new Set(['GET', 'POST', 'PUT', 'PATCH', 'DELETE'])

export async function run(args, env, write) {
  const usage = (msg) => {
    write(`${msg}\nusage: javo-api [GET|POST|PUT|PATCH|DELETE] /api/PATH [JSON]\n`)
    return 2
  }
  if (!env.JAVO_URL || !env.JAVO_TOKEN) return usage('JAVO_URL and JAVO_TOKEN must be set (they are, inside a coding-agent app).')
  const [method, path, body] = METHODS.has((args[0] ?? '').toUpperCase()) ? [args[0].toUpperCase(), args[1], args[2]] : ['GET', args[0], args[1]]
  if (!path || !path.startsWith('/')) return usage('Give a path such as /api/me.')
  if (body !== undefined) {
    try {
      JSON.parse(body)
    } catch {
      return usage('The body is not JSON.')
    }
  }
  const res = await fetch(new URL(path, env.JAVO_URL), {
    method,
    headers: { Authorization: `Bearer ${env.JAVO_TOKEN}`, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body,
  })
  const text = await res.text()
  let shown = text
  try {
    shown = JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    // Not JSON: print it as it came.
  }
  if (!res.ok) write(`HTTP ${res.status}\n`)
  // Hide the token if an answer ever echoes it (real tokens are long; a
  // short value would blank out ordinary letters).
  write((env.JAVO_TOKEN.length >= 16 ? shown.replaceAll(env.JAVO_TOKEN, '[token]') : shown) + '\n')
  return res.ok ? 0 : 1
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  run(process.argv.slice(2), process.env, (s) => process.stdout.write(s)).then(
    (code) => process.exit(code),
    (err) => {
      process.stderr.write(`javo-api: ${err.message}\n`)
      process.exit(1)
    },
  )
}
