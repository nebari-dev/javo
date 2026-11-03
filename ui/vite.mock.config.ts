import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Dev-only server for the UI against canned API answers (mock/main.ts).
export default defineConfig({
  root: 'mock',
  plugins: [react()],
  server: { port: 5174, strictPort: true },
})
