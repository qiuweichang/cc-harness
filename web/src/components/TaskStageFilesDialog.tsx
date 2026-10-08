import { ExternalLink, FileText, FolderOpen, LoaderCircle, Search, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { listTaskFiles, openTaskFile } from '../api'
import type { TaskFile } from '../types'

export interface StageFileDefinition {
  key: string
  label: string
  phases: string[]
}

interface TaskStageFilesDialogProps {
  taskId: string
  rootPath: string
  stages: StageFileDefinition[]
  onClose: () => void
}

interface StageFileGroup {
  key: string
  label: string
  artifacts: TaskFile[]
}

// TaskStageFilesDialog groups generated task artifacts by product-development
// stage and reveals the original file through the local control plane.
export function TaskStageFilesDialog({ taskId, rootPath, stages, onClose }: TaskStageFilesDialogProps) {
  const [files, setFiles] = useState<TaskFile[]>([])
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [openingPath, setOpeningPath] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const groups = useMemo(() => groupStageFiles(files, stages, query), [files, stages, query])

  // Refresh artifact metadata while the dialog is open so documents created by
  // a running phase appear without requiring the task page to reload.
  useEffect(() => {
    let cancelled = false
    const refreshFiles = async () => {
      try {
        const items = await listTaskFiles(taskId)
        if (!cancelled) {
          setFiles(items)
          setError(null)
        }
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '无法读取阶段文件')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void refreshFiles()
    const timer = window.setInterval(() => void refreshFiles(), 5000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [taskId])

  // revealFile asks the backend to validate and locate the original file in the
  // host file manager; browser-provided file URLs are intentionally not trusted.
  async function revealFile(path: string) {
    setOpeningPath(path)
    setError(null)
    try {
      await openTaskFile(taskId, path)
    } catch (openError) {
      setError(openError instanceof Error ? openError.message : '无法打开原文件位置')
    } finally {
      setOpeningPath(null)
    }
  }

  return (
    <div className="dialog-backdrop task-inspector-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="dialog-card task-inspector-dialog stage-files-dialog" role="dialog" aria-modal="true" aria-labelledby="stage-files-title">
        <header className="task-inspector-header">
          <div><span className="eyebrow">STAGE ARTIFACTS</span><h2 id="stage-files-title">阶段文件</h2><p>前后端项目按文件夹展示，其余文档按阶段逐文件展示。</p></div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭阶段文件"><X size={18} /></button>
        </header>
        <div className="stage-files-toolbar"><label><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索阶段文件或路径" /></label><code title={rootPath}>{rootPath}</code></div>
        {error && <div className="page-error inspector-error"><span>{error}</span><button type="button" onClick={() => setError(null)}><X size={13} /></button></div>}
        <div className="stage-files-content">
          {loading ? <div className="inspector-loading"><LoaderCircle size={19} />正在扫描任务输出…</div> : groups.length === 0 ? <div className="stage-files-empty"><FolderOpen size={24} /><strong>暂无阶段输出文件</strong><p>智能体生成正式产物后会自动出现在这里。</p></div> : groups.map((group) => (
            <section className="stage-file-group" key={group.key}>
              <header><div><FolderOpen size={15} /><strong>{group.label}</strong></div><span>{group.artifacts.length} 项</span></header>
              <div>{group.artifacts.map((artifact) => (
                <article className={artifact.directory ? 'is-project-folder' : ''} key={artifact.path}>
                  <span className="stage-file-icon">{artifact.directory ? <FolderOpen size={16} /> : <FileText size={15} />}</span>
                  <div><strong>{artifact.directory ? projectFolderLabel(artifact.path) : artifact.path.split('/').pop()}</strong><code>{absoluteTaskPath(rootPath, artifact.path)}</code></div>
                  <aside><small>{artifact.directory ? '项目文件夹' : `${formatFileSize(artifact.size)} · ${formatFileTime(artifact.updatedAt)}`}</small><button type="button" disabled={openingPath === artifact.path} onClick={() => void revealFile(artifact.path)}>{openingPath === artifact.path ? <LoaderCircle size={13} /> : <ExternalLink size={13} />}{artifact.directory ? '打开文件夹' : '定位原文件'}</button></aside>
                </article>
              ))}</div>
            </section>
          ))}
        </div>
      </section>
    </div>
  )
}

// groupStageFiles filters control files, classifies generated artifacts, and only
// returns stages that currently contain output.
function groupStageFiles(files: TaskFile[], stages: StageFileDefinition[], query: string): StageFileGroup[] {
  const searchable = query.trim().toLowerCase()
  const projectFolders = files.filter((file) => {
    const lower = file.path.replaceAll('\\', '/').toLowerCase()
    return file.directory && (lower === 'frontend' || lower === 'backend') && (!searchable || lower.includes(searchable) || projectFolderLabel(lower).includes(searchable))
  })
  const eligibleDocuments = files.filter((file) => {
    const path = file.path.replaceAll('\\', '/')
    const lower = path.toLowerCase()
    if (file.directory || lower === 'task.json') return false
    if (lower.startsWith('.cc-hardness/') || lower.startsWith('.claude/') || lower.startsWith('.git/') || lower.startsWith('node_modules/') || lower.startsWith('inputs/')) return false
    const documentExtensions = /\.(md|mdx|txt|pdf|docx|png|jpe?g|webp|svg|csv|xlsx|pptx|drawio|mmd)$/
    const topLevelDocument = !lower.includes('/') && documentExtensions.test(lower)
    if (!lower.startsWith('docs/') && !topLevelDocument) return false
    return !searchable || lower.includes(searchable)
  })
  const groups = new Map<string, TaskFile[]>()
  for (const file of eligibleDocuments) {
    const stage = classifyStageFile(file.path, stages)
    if (!stage || stage.key === 'TEAM') continue
    groups.set(stage.key, [...(groups.get(stage.key) ?? []), file])
  }
  const stageGroups = stages
    .filter((stage) => stage.key !== 'TEAM' && (groups.get(stage.key)?.length ?? 0) > 0)
    .map((stage) => ({
      key: stage.key,
      label: stage.label,
      artifacts: (groups.get(stage.key) ?? []).sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt)),
    }))
  return projectFolders.length > 0
    ? [{ key: 'PROJECTS', label: '项目源码', artifacts: projectFolders }, ...stageGroups]
    : stageGroups
}

// projectFolderLabel gives generated project roots a clear product-facing name.
function projectFolderLabel(path: string): string {
  return path.replaceAll('\\', '/').toLowerCase() === 'frontend' ? '前端项目' : '后端项目'
}

// classifyStageFile recognizes the current docs/agents/<agent>/<phase> convention
// and the legacy docs/stages path so old and newly generated tasks stay visible.
function classifyStageFile(path: string, stages: StageFileDefinition[]): StageFileDefinition | null {
  const lower = `/${path.replaceAll('\\', '/').toLowerCase()}/`
  for (const stage of stages) {
    for (const phase of stage.phases) {
      const phaseDirectory = phase.toLowerCase().replaceAll('_', '-')
      if (lower.includes(`/docs/stages/${phaseDirectory}/`) || (lower.includes('/docs/agents/') && lower.includes(`/${phaseDirectory}/`))) return stage
    }
  }
  const findStage = (key: string) => stages.find((stage) => stage.key === key) ?? null
  if (/requirements?|需求|prd|product|alignment/.test(lower)) return findStage('ALIGNMENT')
  if (/architect|架构|database|数据库|schema|openapi|api-design|adr/.test(lower)) return findStage('ARCHITECTURE')
  if (/prototype|原型|wireframe|mockup|ui-design|design\.md|screenshots?|设计稿/.test(lower)) return findStage('PROTOTYPE')
  if (/quality|质量|audit|审计/.test(lower)) return findStage('QUALITY')
  if (/integration|联调|smoke/.test(lower)) return findStage('INTEGRATION')
  if (/tests?|测试|playwright|coverage|测试报告/.test(lower)) return findStage('TEST')
  if (/delivery|交付|deploy|部署|release/.test(lower)) return findStage('DELIVERY')
  return findStage('DEVELOPMENT') ?? stages.find((stage) => stage.key !== 'TEAM') ?? null
}

// absoluteTaskPath creates a readable host path without using it for API access.
function absoluteTaskPath(rootPath: string, relativePath: string): string {
  const separator = rootPath.includes('\\') ? '\\' : '/'
  return `${rootPath.replace(/[\\/]$/, '')}${separator}${relativePath.replaceAll('/', separator)}`
}

// formatFileSize renders compact binary sizes for artifact rows.
function formatFileSize(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

// formatFileTime renders the artifact update time in the user's local timezone.
function formatFileTime(value: string): string {
  return new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
