import { AlertTriangle, Info, Trash2, X } from 'lucide-react'
import {
  createContext, useCallback, useContext, useEffect, useMemo, useRef, useState,
  type ReactNode,
} from 'react'

type DialogTone = 'default' | 'danger' | 'info'

interface DialogOptions {
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  tone?: DialogTone
}

interface ActiveDialog extends DialogOptions {
  kind: 'alert' | 'confirm'
}

interface AppDialogAPI {
  alert: (options: DialogOptions) => Promise<void>
  confirm: (options: DialogOptions) => Promise<boolean>
}

const AppDialogContext = createContext<AppDialogAPI | null>(null)

// AppDialogProvider replaces browser-native alert and confirm windows with one
// consistent application modal while preserving awaitable action semantics.
export function AppDialogProvider({ children }: { children: ReactNode }) {
  const [active, setActive] = useState<ActiveDialog | null>(null)
  // resolverRef completes the pending caller exactly once when the modal closes.
  const resolverRef = useRef<((accepted: boolean) => void) | null>(null)

  // finish resolves the caller before clearing visual state so no action can run
  // twice from a rapid button or Escape interaction.
  const finish = useCallback((accepted: boolean) => {
    const resolve = resolverRef.current
    resolverRef.current = null
    setActive(null)
    resolve?.(accepted)
  }, [])

  // open serializes modal requests. A superseded dialog is treated as cancelled
  // instead of leaving its Promise unresolved.
  const open = useCallback((dialog: ActiveDialog) => new Promise<boolean>((resolve) => {
    resolverRef.current?.(false)
    resolverRef.current = resolve
    setActive(dialog)
  }), [])

  // alert displays informational content and resolves after acknowledgment.
  const alert = useCallback(async (options: DialogOptions) => {
    await open({ ...options, kind: 'alert', tone: options.tone ?? 'info' })
  }, [open])

  // confirm displays a cancellable decision and returns the user's choice.
  const confirm = useCallback((options: DialogOptions) => (
    open({ ...options, kind: 'confirm', tone: options.tone ?? 'default' })
  ), [open])

  useEffect(() => {
    if (!active) return
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') finish(false)
      if (event.key === 'Enter') finish(true)
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [active, finish])

  useEffect(() => () => resolverRef.current?.(false), [])

  const api = useMemo(() => ({ alert, confirm }), [alert, confirm])
  const Icon = active?.tone === 'danger' ? Trash2 : active?.tone === 'info' ? Info : AlertTriangle

  return (
    <AppDialogContext.Provider value={api}>
      {children}
      {active && (
        <div className="dialog-backdrop app-prompt-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && finish(false)}>
          <section className={`app-prompt-dialog tone-${active.tone ?? 'default'}`} role={active.kind === 'alert' ? 'alertdialog' : 'dialog'} aria-modal="true" aria-labelledby="app-prompt-title" aria-describedby="app-prompt-message">
            <header>
              <span className="app-prompt-icon"><Icon size={20} /></span>
              <div><h2 id="app-prompt-title">{active.title}</h2></div>
              <button className="icon-button" type="button" onClick={() => finish(false)} aria-label="关闭提示"><X size={17} /></button>
            </header>
            <p id="app-prompt-message">{active.message}</p>
            <footer>
              {active.kind === 'confirm' && <button className="button secondary" type="button" onClick={() => finish(false)}>{active.cancelText ?? '取消'}</button>}
              <button className={`button ${active.tone === 'danger' ? 'danger-solid-button' : 'primary'}`} type="button" onClick={() => finish(true)} autoFocus>{active.confirmText ?? (active.kind === 'alert' ? '知道了' : '确认')}</button>
            </footer>
          </section>
        </div>
      )}
    </AppDialogContext.Provider>
  )
}

// useAppDialog exposes the shared modal contract and fails early when a component
// is accidentally rendered outside the application provider.
export function useAppDialog(): AppDialogAPI {
  const value = useContext(AppDialogContext)
  if (!value) throw new Error('useAppDialog must be used within AppDialogProvider')
  return value
}
