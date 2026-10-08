import { ArrowRight, FileText, FolderPlus, Paperclip, Upload, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent } from 'react'
import type { NewTaskRequest, TaskGroup } from '../types'

interface NewTaskDialogProps {
  open: boolean
  submitting: boolean
  error: string | null
  groups: TaskGroup[]
  onClose: () => void
  onSubmit: (input: NewTaskRequest) => Promise<void>
}

// NewTaskDialog collects the project context, group placement, and source files required
// before the AI planning flow starts. It intentionally skips a summary and delivery URL step.
export function NewTaskDialog({ open, submitting, error, groups, onClose, onSubmit }: NewTaskDialogProps) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [groupMode, setGroupMode] = useState<'existing' | 'new'>(groups.length ? 'existing' : 'new')
  const [groupId, setGroupId] = useState(groups[0]?.id ?? '')
  const [groupName, setGroupName] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const inputRef = useRef<HTMLInputElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const selectedGroup = useMemo(() => groups.find((group) => group.id === groupId), [groupId, groups])

  // Reset transient browser state whenever a new creation flow starts. Focus
  // immediately after the dialog is committed so a delayed callback cannot
  // steal focus from a user who has already moved to the description field.
  useEffect(() => {
    if (!open) return
    setName('')
    setDescription('')
    setFiles([])
    setGroupMode(groups.length ? 'existing' : 'new')
    setGroupId(groups[0]?.id ?? '')
    setGroupName('')
    inputRef.current?.focus()
    // The form is intentionally keyed only to the open transition. Background
    // task/group refreshes must not erase text while the user is editing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  // addFiles de-duplicates files by the browser-visible name, size, and modification time.
  function addFiles(event: ChangeEvent<HTMLInputElement>) {
    const incoming = Array.from(event.target.files ?? [])
    setFiles((current) => {
      const known = new Set(current.map((file) => `${file.name}:${file.size}:${file.lastModified}`))
      return [...current, ...incoming.filter((file) => !known.has(`${file.name}:${file.size}:${file.lastModified}`))]
    })
    event.target.value = ''
  }

  // handleSubmit passes files and normalized task metadata to the parent orchestration flow.
  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim() || submitting) return
    const resolvedGroupName = groupMode === 'existing' ? selectedGroup?.name ?? '默认任务组' : groupName.trim()
    if (groupMode === 'new' && !resolvedGroupName) return
    await onSubmit({
      name: name.trim(),
      description: description.trim(),
      groupId: groupMode === 'existing' ? groupId : '',
      groupName: resolvedGroupName,
      files,
    })
  }

  if (!open) return null

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="dialog-card new-task-dialog" role="dialog" aria-modal="true" aria-labelledby="new-task-title">
        <div className="dialog-header">
          <div>
            <span className="eyebrow">AI TEAM WORKSPACE</span>
            <h2 id="new-task-title">创建任务并自动组建团队</h2>
          </div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭"><X size={18} /></button>
        </div>

        <form onSubmit={handleSubmit}>
          <div className="form-grid two">
            <label><span>任务名称</span><input ref={inputRef} maxLength={120} placeholder="例如：客户管理系统" value={name} onChange={(event) => setName(event.target.value)} /></label>
            <label><span>任务组</span>
              <div className="segmented-control">
                <button className={groupMode === 'existing' ? 'active' : ''} type="button" disabled={!groups.length} onClick={() => setGroupMode('existing')}>使用已有</button>
                <button className={groupMode === 'new' ? 'active' : ''} type="button" onClick={() => setGroupMode('new')}>创建新组</button>
              </div>
            </label>
          </div>

          {groupMode === 'existing' ? (
            <label className="form-field"><span>选择任务组</span><select value={groupId} onChange={(event) => setGroupId(event.target.value)}>{groups.map((group) => <option value={group.id} key={group.id}>{group.name}（{group.tasks.length}）</option>)}</select></label>
          ) : (
            <label className="form-field"><span>新任务组名称</span><div className="input-with-icon"><FolderPlus size={16} /><input maxLength={80} value={groupName} onChange={(event) => setGroupName(event.target.value)} placeholder="例如：电商平台一期" /></div></label>
          )}

          <label className="field-label" htmlFor="task-description">项目背景与目标</label>
          <textarea id="task-description" className="text-area" maxLength={8000} placeholder="描述要解决的问题、核心功能、约束和期望结果……" value={description} onChange={(event) => setDescription(event.target.value)} />

          <input ref={fileInputRef} className="hidden-input" type="file" multiple onChange={addFiles} />
          <button className="upload-dropzone" type="button" onClick={() => fileInputRef.current?.click()}>
            <span><Upload size={19} /></span>
            <div><strong>上传项目资料</strong><small>支持 PRD、Markdown、图片、压缩包及代码文件，单个请求最大 32MB</small></div>
            <Paperclip size={16} />
          </button>
          {files.length > 0 && <div className="new-task-files">{files.map((file, index) => <div key={`${file.name}-${file.lastModified}`}><FileText size={14} /><span>{file.name}</span><small>{Math.max(1, Math.ceil(file.size / 1024))} KB</small><button type="button" onClick={() => setFiles((current) => current.filter((_, itemIndex) => itemIndex !== index))}><X size={13} /></button></div>)}</div>}

          {error && <div className="form-error">{error}</div>}

          <div className="dialog-actions">
            <button className="button secondary" type="button" onClick={onClose}>取消</button>
            <button className="button primary" type="submit" disabled={!name.trim() || (groupMode === 'new' && !groupName.trim()) || (groupMode === 'existing' && !groupId) || submitting}>
              <span>{submitting ? 'AI 正在组建团队…' : '开始任务'}</span>
              {!submitting && <ArrowRight size={16} />}
            </button>
          </div>
        </form>
      </section>
    </div>
  )
}
