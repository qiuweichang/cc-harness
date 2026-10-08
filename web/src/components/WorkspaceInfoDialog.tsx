import { Bot, HardDrive, LoaderCircle, ShieldCheck, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { getClaudeInfo } from '../api'
import type { ClaudeInfo } from '../types'

interface WorkspaceInfoDialogProps {
  open: boolean
  onClose: () => void
}

// WorkspaceInfoDialog presents the local Claude Code runtime configuration in
// the shared in-app dialog style without exposing credentials or provider URLs.
export function WorkspaceInfoDialog({ open, onClose }: WorkspaceInfoDialogProps) {
  const [info, setInfo] = useState<ClaudeInfo | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Reload runtime information on every open so upgrades and model changes are
  // visible without requiring a full browser refresh.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    setInfo(null)
    setError(null)
    getClaudeInfo()
      .then((result) => !cancelled && setInfo(result))
      .catch((loadError) => !cancelled && setError(loadError instanceof Error ? loadError.message : '无法读取本地运行配置'))
    return () => {
      cancelled = true
    }
  }, [open])

  if (!open) return null

  return (
    <div className="dialog-backdrop workspace-info-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="dialog-card workspace-info-dialog" role="dialog" aria-modal="true" aria-labelledby="workspace-info-title">
        <header>
          <span><HardDrive size={20} /></span>
          <div><span className="eyebrow">LOCAL WORKSPACE</span><h2 id="workspace-info-title">本地工作区</h2></div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭"><X size={17} /></button>
        </header>

        {!info && !error ? (
          <div className="workspace-info-loading"><LoaderCircle size={18} />正在读取运行配置…</div>
        ) : error ? (
          <div className="form-error workspace-info-error">{error}</div>
        ) : info && (
          <div className="workspace-info-content">
            <article>
              <span><Bot size={17} /></span>
              <div><small>Claude Code 版本</small><strong>{info.detected ? info.version : '未检测到 Claude Code'}</strong></div>
            </article>
            <article>
              <span><Bot size={17} /></span>
              <div><small>当前调用模型</small><strong>{info.model}</strong><em>{info.modelSource}</em></div>
            </article>
            <article>
              <span><ShieldCheck size={17} /></span>
              <div><small>智能体运行配置</small><strong>完全编辑权限</strong><em>{info.permissionMode} · {info.inputFormat} / {info.outputFormat}</em></div>
            </article>
          </div>
        )}

        <footer><button className="button primary" type="button" onClick={onClose}>完成</button></footer>
      </section>
    </div>
  )
}
