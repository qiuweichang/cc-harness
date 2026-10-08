import { ArrowUpRight, CalendarDays, ChevronLeft, ChevronRight, FolderOpen, GitBranch, LoaderCircle, MoreHorizontal, Search, Sparkles, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import type { Task, TaskGroup } from '../types'

interface TaskHomeProps {
  groups: TaskGroup[]
  selectedGroupId: string | null
  selectedTask: Task | null
  loading: boolean
  error: string | null
  deletingTaskId: string | null
  onSelect: (taskId: string) => void
  onOpen: (taskId: string) => void
  onDelete: (taskId: string) => void
  onNewTask: () => void
}

const statusLabels: Record<string, string> = {
  DRAFT: '待规划', PLANNING: '规划中', READY: '待启动', RUNNING: '运行中',
  PAUSED: '已暂停', ACCEPTANCE: '待验收', COMPLETED: '已完成', FAILED: '异常',
  CLOSED: '已关闭',
}

// formatDate provides a stable compact date for task cards while the full timestamp remains available.
function formatDate(value: string): string {
  return new Date(value).toLocaleDateString('zh-CN', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

// TaskHome renders the selected task group as a searchable card dashboard.
export function TaskHome({ groups, selectedGroupId, selectedTask, loading, error, deletingTaskId, onSelect, onOpen, onDelete, onNewTask }: TaskHomeProps) {
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(1)
  const [jumpPage, setJumpPage] = useState('')
  // menuTaskId keeps destructive actions out of the card's primary navigation
  // while ensuring that only one compact overflow menu can be open at a time.
  const [menuTaskId, setMenuTaskId] = useState<string | null>(null)
  const selectedGroup = groups.find((group) => group.id === selectedGroupId) ?? groups[0] ?? null
  const pageSize = 9
  const filteredTasks = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!selectedGroup) return []
    if (!normalized) return selectedGroup.tasks
    return selectedGroup.tasks.filter((task) => `${task.name} ${task.description} ${task.status} ${task.phase}`.toLowerCase().includes(normalized))
  }, [query, selectedGroup])
  const pageCount = Math.max(1, Math.ceil(filteredTasks.length / pageSize))
  const pagedTasks = filteredTasks.slice((page - 1) * pageSize, page * pageSize)
  const visiblePages = Array.from({ length: Math.min(5, pageCount) }, (_, index) => Math.max(1, Math.min(pageCount - 4, page - 2)) + index).filter((value) => value <= pageCount)

  // Search and group changes restart pagination so valid results are never hidden off-page.
  useEffect(() => {
    setPage(1)
    setJumpPage('')
  }, [query, selectedGroup?.id])

  // Live task updates may shrink the result set while the user is on its last page.
  useEffect(() => {
    setPage((current) => Math.min(current, pageCount))
  }, [pageCount])

  // Clicking elsewhere or pressing Escape dismisses the task overflow menu without
  // opening the underlying workspace card.
  useEffect(() => {
    if (!menuTaskId) return
    const closeMenu = () => setMenuTaskId(null)
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeMenu()
    }
    document.addEventListener('click', closeMenu)
    window.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('click', closeMenu)
      window.removeEventListener('keydown', closeOnEscape)
    }
  }, [menuTaskId])

  // jumpToPage clamps free-form input to the available page range.
  function jumpToPage() {
    const target = Number.parseInt(jumpPage, 10)
    if (!Number.isFinite(target)) return
    setPage(Math.max(1, Math.min(pageCount, target)))
    setJumpPage('')
  }

  return (
    <main className="task-group-dashboard">
      <header className="content-header group-dashboard-header">
        <div>
          <span className="eyebrow">TASK GROUP</span>
          <h1>{selectedGroup?.name ?? '任务组'}</h1>
        </div>
      </header>

      {selectedGroup && <div className="group-dashboard-toolbar">
        <div className="group-search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索任务名称、背景、阶段或状态" /><kbd>{filteredTasks.length}</kbd></div>
        <span>最近更新 {formatDate(selectedGroup.updatedAt)}</span>
      </div>}

      {loading ? (
        <div className="loading-list" aria-label="正在加载任务"><span /><span /><span /></div>
      ) : error ? (
        <div className="inline-state error-state"><strong>暂时无法读取任务</strong><p>{error}</p></div>
      ) : !selectedGroup ? (
        <button className="empty-task-card" type="button" onClick={onNewTask}><span className="empty-task-icon"><Sparkles size={21} /></span><strong>创建第一个任务组和任务</strong><p>填写项目背景并上传资料，AI 将自动组建团队和工作流。</p><span className="empty-task-action">开始创建 <ArrowUpRight size={14} /></span></button>
      ) : filteredTasks.length === 0 ? (
        <div className="inline-state"><Search size={20} /><strong>{selectedGroup.tasks.length === 0 ? '该任务组暂无任务' : '没有匹配的任务'}</strong><p>{selectedGroup.tasks.length === 0 ? '任务组会继续保留，可在新建任务时再次选择。' : '换一个关键词，或在该任务组中新建任务。'}</p></div>
      ) : (
        <section className="task-card-grid" aria-label={`${selectedGroup.name}任务列表`}>
          {pagedTasks.map((task) => (
            <article className={`task-work-card ${selectedTask?.id === task.id ? 'selected' : ''}`} key={task.id} onClick={() => { onSelect(task.id); onOpen(task.id) }}>
              <div className="task-work-card-heading">
                <span className="task-row-icon"><FolderOpen size={18} /></span>
                <div><h2>{task.name}</h2><small>{task.id}</small></div>
                <div className="task-card-menu" onClick={(event) => event.stopPropagation()}>
                  <button
                    className="task-card-menu-trigger"
                    type="button"
                    disabled={deletingTaskId === task.id}
                    aria-label={`${task.name}更多操作`}
                    aria-haspopup="menu"
                    aria-expanded={menuTaskId === task.id}
                    onClick={(event) => {
                      event.stopPropagation()
                      setMenuTaskId((current) => current === task.id ? null : task.id)
                    }}
                  >
                    {deletingTaskId === task.id ? <LoaderCircle className="spinning-icon" size={17} /> : <MoreHorizontal size={18} />}
                  </button>
                  {menuTaskId === task.id && (
                    <div className="task-card-menu-popover" role="menu">
                      <button type="button" role="menuitem" onClick={() => { setMenuTaskId(null); onDelete(task.id) }}>
                        <Trash2 size={14} />删除任务
                      </button>
                    </div>
                  )}
                </div>
              </div>
              <p>{task.description || '尚未填写项目背景。'}</p>
              <dl>
                <div><dt><GitBranch size={13} />当前阶段</dt><dd>{task.phase}</dd></div>
                <div><dt><CalendarDays size={13} />最近更新</dt><dd>{formatDate(task.updatedAt)}</dd></div>
              </dl>
              <div className="task-card-footer">
                <span className={`status-chip status-${task.status.toLowerCase()}`}><span />{statusLabels[task.status] ?? task.status}</span>
                <button className="button secondary compact" type="button" onClick={(event) => { event.stopPropagation(); onOpen(task.id) }}>打开工作区 <ArrowUpRight size={14} /></button>
              </div>
            </article>
          ))}
        </section>
      )}
      {selectedGroup && filteredTasks.length > 0 && <nav className="task-pagination" aria-label="任务分页">
        <button type="button" disabled={page === 1} onClick={() => setPage((current) => Math.max(1, current - 1))}><ChevronLeft size={14} />上一页</button>
        <div>{visiblePages.map((pageNumber) => <button className={pageNumber === page ? 'active' : ''} type="button" key={pageNumber} onClick={() => setPage(pageNumber)}>{pageNumber}</button>)}</div>
        <button type="button" disabled={page === pageCount} onClick={() => setPage((current) => Math.min(pageCount, current + 1))}>下一页<ChevronRight size={14} /></button>
        <span>第 {page} / {pageCount} 页</span>
        <label>跳至<input value={jumpPage} inputMode="numeric" onChange={(event) => setJumpPage(event.target.value.replace(/\D/g, ''))} onKeyDown={(event) => event.key === 'Enter' && jumpToPage()} />页<button type="button" onClick={jumpToPage}>跳转</button></label>
      </nav>}
    </main>
  )
}
