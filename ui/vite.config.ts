import react from '@vitejs/plugin-react'
import { loadEnv } from 'vite'
import { defineConfig } from 'vitest/config'

// The routes the gateway answers. With JAVO_DEV_PROXY set (a gateway URL,
// e.g. http://k3s:30080), `vite` serves the UI with hot reload and sends
// these to the gateway, keeping the browser's Host header so the gateway
// sees its UI host (scripts/dev/remote-up.sh).
const gatewayRoutes = ['/api', '/auth', '/u/', '/branding', '/repos', '/mcp', '/.well-known', '/healthz', '/readyz']

export default defineConfig(({ mode }) => {
  const gateway = loadEnv(mode, '.', 'JAVO_DEV_').JAVO_DEV_PROXY
  const proxy = gateway ? Object.fromEntries(gatewayRoutes.map((r) => [r, { target: gateway, ws: true }])) : undefined
  return {
    plugins: [react()],
    // The CSP allows images from 'self' only, so SVGs ship as files, never data: URIs.
    build: { assetsInlineLimit: (file) => (file.endsWith('.svg') ? false : undefined) },
    // KindLogo.test.tsx reads the kind enum from the Go model.
    server: { fs: { allow: ['.', '../internal/model'] }, proxy },
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      // Off, except theme.css, whose tokens theme.test.ts reads (?raw).
      css: { include: [/theme\.css/] },
    },
  }
})
