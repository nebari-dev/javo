// Copies the Swagger UI files the gateway serves at /api/docs/ (when
// api.docs.enabled) from the pinned swagger-ui-dist package into the built
// UI, so the page never loads anything from a CDN.
import { copyFileSync, mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

const require = createRequire(import.meta.url)
const src = dirname(require.resolve('swagger-ui-dist/package.json'))
const dest = join(import.meta.dirname, '..', 'dist', 'apidocs')
mkdirSync(dest, { recursive: true })
for (const file of ['swagger-ui-bundle.js', 'swagger-ui.css']) copyFileSync(join(src, file), join(dest, file))
