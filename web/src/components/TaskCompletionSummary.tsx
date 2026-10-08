import { CheckCircle2, FileCheck2, FolderCode, FolderOpen, LoaderCircle } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { listTaskFiles } from '../api'
import type { TaskDetail, TaskFile, WorkflowNode } from '../types'

export interface CompletionStageDefinition {
  key: string
  label: string
  phases: string[]
}

interface TaskCompletionSummaryProps {
  detail: TaskDetail
  stages: CompletionStageDefinition[]
  mode: 'delivery' | 'completed'
  onOpenFiles: () => void
}

interface WorkStageSummary {
  key: string
  label: string
  nodes: WorkflowNode[]
  succeeded: number
  failed: number
}

interface DocumentStageSummary {
  key: string
  label: string
  files: TaskFile[]
}

const terminalSuccessStates = new Set(['SUCCEEDED', 'SKIPPED'])

// TaskCompletionSummary presents durable workflow and document output without
// reopening editable DAG controls. Delivery mode supports final review while
// completed mode becomes the permanent read-only success overview.
export function TaskCompletionSummary({ detail, stages, mode, onOpenFiles }: TaskCompletionSummaryProps) {
  const [files, setFiles] = useState<TaskFile[]>([])
  const [loadingFiles, setLoadingFiles] = useState(true)

  // Load artifact metadata once per durable revision; stage-file inspection
  // remains available separately for live refresh and original-path navigation.
  useEffect(() => {
    let cancelled = false
    setLoadingFiles(true)
    listTaskFiles(detail.id)
      .then((items) => {
        if (!cancelled) setFiles(items)
      })
      .catch(() => {
        if (!cancelled) setFiles([])
      })
      .finally(() => {
        if (!cancelled) setLoadingFiles(false)
      })
    return () => {
      cancelled = true
    }
  }, [detail.id, detail.revision])

  const workStages = useMemo(() => buildWorkStageSummaries(detail, stages), [detail.plan, detail.runtime, stages])
  const documentStages = useMemo(() => buildDocumentStageSummaries(files, stages), [files, stages])
  const projectFolders = files.filter((file) => file.directory && ['frontend', 'backend'].includes(normalizePath(file.path)))
  const totalWork = workStages.reduce((total, stage) => total + stage.nodes.length, 0)
  const succeededWork = workStages.reduce((total, stage) => total + stage.succeeded, 0)
  const failedWork = workStages.reduce((total, stage) => total + stage.failed, 0)
  const documentCount = documentStages.reduce((total, stage) => total + stage.files.length, 0)

  return (
    <section className={`completion-summary completion-summary-${mode}`}>
      <header className="completion-summary-hero">
        <span><CheckCircle2 size={24} /></span>
        <div>
          <small>{mode === 'completed' ? 'TASK COMPLETED' : 'DELIVERY REVIEW'}</small>
          <h3>{mode === 'completed' ? '所有工作已完成' : '工作与交付文档已汇总'}</h3>
          <p>{mode === 'completed' ? '这里保留整个任务的工作结果和文档输出，可随时查看归档资料。' : '请检查各阶段结果和文档输出；需要调整可继续在下方告诉项目经理，确认无误后点击右上角“确认交付”。'}</p>
        </div>
      </header>

      <div className="completion-metrics">
        <article><strong>{succeededWork}/{totalWork}</strong><span>工作成功</span></article>
        <article className={failedWork > 0 ? 'has-failure' : ''}><strong>{failedWork}</strong><span>异常工作</span></article>
        <article><strong>{documentCount}</strong><span>输出文档</span></article>
        <article><strong>{projectFolders.length}</strong><span>项目文件夹</span></article>
      </div>

      <section className="completion-section">
        <header><div><CheckCircle2 size={17} /><strong>工作完成情况</strong></div><span>{succeededWork} 项成功</span></header>
        <div className="completion-stage-grid">
          {workStages.map((stage) => (
            <article className={stage.failed > 0 ? 'has-failure' : ''} key={stage.key}>
              <header><strong>{stage.label}</strong><span>{stage.succeeded}/{stage.nodes.length}</span></header>
              <div>{stage.nodes.map((node) => {
                const status = detail.runtime?.nodeStates[node.key] ?? 'PENDING'
                return <p key={node.key}><span className={`completion-node-dot state-${status.toLowerCase()}`} /><b>{node.name}</b><small>{workflowStatusLabel(status)}</small></p>
              })}</div>
            </article>
          ))}
        </div>
      </section>

      <section className="completion-section completion-output-section">
        <header><div><FileCheck2 size={17} /><strong>文档与项目输出</strong></div><button type="button" onClick={onOpenFiles}><FolderOpen size={14} />查看阶段文件</button></header>
        {loadingFiles ? <div className="completion-output-loading"><LoaderCircle size={18} />正在汇总输出…</div> : (
          <div className="completion-output-grid">
            {projectFolders.map((folder) => (
              <article className="completion-project-folder" key={folder.path}>
                <span><FolderCode size={19} /></span><div><strong>{normalizePath(folder.path) === 'frontend' ? '前端项目' : '后端项目'}</strong><small>{folder.path}</small></div>
              </article>
            ))}
            {documentStages.map((stage) => (
              <article className="completion-document-group" key={stage.key}>
                <header><strong>{stage.label}</strong><span>{stage.files.length} 份</span></header>
                <div>{stage.files.slice(0, 4).map((file) => <p key={file.path}>{file.path.split('/').pop()}</p>)}</div>
                {stage.files.length > 4 && <small>另有 {stage.files.length - 4} 份文档</small>}
              </article>
            ))}
            {projectFolders.length === 0 && documentStages.length === 0 && <div className="completion-output-empty">暂无已归档的文档或项目文件夹</div>}
          </div>
        )}
      </section>
    </section>
  )
}

// buildWorkStageSummaries groups workflow nodes by their user-facing lifecycle
// stage and derives success/failure counts from the durable runtime state.
function buildWorkStageSummaries(detail: TaskDetail, stages: CompletionStageDefinition[]): WorkStageSummary[] {
  const workflow = detail.plan?.workflow ?? []
  return stages.flatMap((stage) => {
    const nodes = workflow.filter((node) => stage.phases.includes(node.phase))
    if (nodes.length === 0) return []
    const statuses = nodes.map((node) => detail.runtime?.nodeStates[node.key] ?? 'PENDING')
    return [{
      key: stage.key,
      label: stage.label,
      nodes,
      succeeded: statuses.filter((status) => terminalSuccessStates.has(status)).length,
      failed: statuses.filter((status) => status === 'FAILED').length,
    }]
  })
}

// buildDocumentStageSummaries groups current agent-owned document folders and
// legacy stage folders while excluding source trees and runtime control files.
function buildDocumentStageSummaries(files: TaskFile[], stages: CompletionStageDefinition[]): DocumentStageSummary[] {
  const documents = files.filter((file) => !file.directory && normalizePath(file.path).startsWith('docs/'))
  return stages.flatMap((stage) => {
    const stageFiles = documents.filter((file) => {
      const path = `/${normalizePath(file.path)}/`
      return stage.phases.some((phase) => {
        const phaseDirectory = phase.toLowerCase().replaceAll('_', '-')
        return path.includes(`/docs/stages/${phaseDirectory}/`) || (path.includes('/docs/agents/') && path.includes(`/${phaseDirectory}/`))
      })
    })
    return stageFiles.length > 0 ? [{ key: stage.key, label: stage.label, files: stageFiles }] : []
  })
}

// normalizePath keeps comparisons stable across Windows and Unix task records.
function normalizePath(path: string): string {
  return path.replaceAll('\\', '/').toLowerCase()
}

// workflowStatusLabel translates node execution states for the summary cards.
function workflowStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    PENDING: '待开始',
    READY: '已就绪',
    RUNNING: '进行中',
    WAITING_USER: '待确认',
    SUCCEEDED: '已完成',
    FAILED: '异常',
    SKIPPED: '已跳过',
  }
  return labels[status] ?? status
}
