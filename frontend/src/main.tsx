import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor'
import { App } from './App'
import './index.css'

// Use the pinned local dependency. The default loader downloads Monaco from a
// CDN, which leaves the editor stuck on "Loading Monaco…" on restricted networks.
loader.config({ monaco })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
