// Copies src/ to dist/ with the build time filled in; no dependencies, so
// the build runs with no network.
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'

rmSync('dist', { recursive: true, force: true })
mkdirSync('dist', { recursive: true })
cpSync('src', 'dist', { recursive: true })
const page = readFileSync('dist/index.html', 'utf8')
writeFileSync('dist/index.html', page.replace('{{BUILD}}', 'built-by-javo-static-site-fixture'))
