import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  // The CSP allows images from 'self' only, so SVGs ship as files, never data: URIs.
  build: { assetsInlineLimit: (file) => (file.endsWith('.svg') ? false : undefined) },
  // KindLogo.test.tsx reads the kind enum from the Go model.
  server: { fs: { allow: ['.', '../internal/model'] } },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    // Off, except theme.css, whose tokens theme.test.ts reads (?raw).
    css: { include: [/theme\.css/] },
  },
})
