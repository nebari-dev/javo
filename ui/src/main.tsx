import { QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { AppRoutes } from './app/App'
import { makeQueryClient } from './lib/queries'
import { applyBrand, loadBrand } from './lib/brand'
import { applyTheme, loadTheme } from './lib/theme'
import './styles/fonts.css'
import './styles/theme.css'
import './styles/components.css'
import './styles/pages.css'

applyTheme(loadTheme())

// The branding comes before the first render, so the default logo never flashes.
void loadBrand().then((b) => {
  applyBrand(b)
  createRoot(document.getElementById('root') as HTMLElement).render(
    <StrictMode>
      <QueryClientProvider client={makeQueryClient()}>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
      </QueryClientProvider>
    </StrictMode>,
  )
})
