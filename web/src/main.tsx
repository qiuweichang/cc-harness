import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { AppDialogProvider } from './components/AppDialogProvider'
import './styles.css'

// mountApplication attaches the React workspace shell to the static document root.
function mountApplication() {
  const root = document.getElementById('root')
  if (!root) throw new Error('Application root element is missing')
  createRoot(root).render(
    <StrictMode>
      <AppDialogProvider><App /></AppDialogProvider>
    </StrictMode>,
  )
}

mountApplication()
