import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import './task-ui.css'
import './aliases-ui.css'
import './inbox-ui.css'
import App from './App.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
