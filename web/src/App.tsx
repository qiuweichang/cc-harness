import { X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { createTask, deleteTask, generateTaskPlan, listTaskGroups, listTasks, uploadTaskFile } from './api'
import { AppSidebar } from './components/AppSidebar'
import { useAppDialog } from './components/AppDialogProvider'
import { AssetPage } from './components/AssetPage'
import { ConfigurationPage } from './components/ConfigurationPage'
import { NewTaskDialog } from './components/NewTaskDialog'
import { TaskHome } from './components/TaskHome'
import { TaskWorkspace } from './components/TaskWorkspace'
import { WorkspaceInfoDialog } from './components/WorkspaceInfoDialog'
import type { NewTaskRequest, SectionKey, Task, TaskGroup, TaskGroupSummary } from './types'

interface AppTab {
  id: string
  kind: 'section' | 'task'
  label: string
  section?: SectionKey
  taskId?: string
}

const sectionLabels: Record<SectionKey, string> = {
  tasks: '任务',
  agents: '智能体管理',
  skills: '技能管理',
  rules: '规则管理',
  knowledge: '知识库管理',
  configurations: '配置管理',
}

// RECENT_TASKS_STORAGE_KEY versions the compact localStorage schema so future
// navigation changes can migrate it without retaining incompatible payloads.
const RECENT_TASKS_STORAGE_KEY = 'cc-hardness:recent-task-ids:v1'

// App coordinates file-backed tasks and an application-level tab strip that preserves
// navigation context when users move between a task phase and catalog management.
export default function App() {
  const appDialog = useAppDialog()
  const [tasks, setTasks] = useState<Task[]>([])
  const [taskGroups, setTaskGroups] = useState<TaskGroupSummary[]>([])
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null)
  const [selectedGroupId, setSelectedGroupId] = useState<string | null>(null)
  const [tabs, setTabs] = useState<AppTab[]>([{ id: 'section:tasks', kind: 'section', section: 'tasks', label: '任务' }])
  const [activeTabId, setActiveTabId] = useState('section:tasks')
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  // workspaceInfoOpen controls the non-secret local Claude runtime summary.
  const [workspaceInfoOpen, setWorkspaceInfoOpen] = useState(false)
  // recentTaskIds stores only stable IDs; current task names and states are
  // derived from the latest server list to avoid stale duplicated task data.
  const [recentTaskIds, setRecentTaskIds] = useState<string[]>(() => {
    try {
      const parsed = JSON.parse(window.localStorage.getItem(RECENT_TASKS_STORAGE_KEY) ?? '[]')
      return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string').slice(0, 6) : []
    } catch {
      return []
    }
  })
  // deletingTaskId locks only the affected card while its processes and workspace
  // are being removed, leaving pagination and other tasks responsive.
  const [deletingTaskId, setDeletingTaskId] = useState<string | null>(null)

  const activeTab = tabs.find((tab) => tab.id === activeTabId) ?? tabs[0]
  const activeSection: SectionKey = activeTab?.kind === 'section' ? activeTab.section ?? 'tasks' : 'tasks'
  const selectedTask = useMemo(() => tasks.find((task) => task.id === selectedTaskId) ?? null, [selectedTaskId, tasks])
  const recentTasks = useMemo(() => {
    const taskById = new Map(tasks.map((task) => [task.id, task]))
    return recentTaskIds.flatMap((id) => {
      const task = taskById.get(id)
      return task ? [task] : []
    })
  }, [recentTaskIds, tasks])
  const groups = useMemo<TaskGroup[]>(() => {
    const grouped = new Map<string, TaskGroup>()
    for (const group of taskGroups) {
      grouped.set(group.id, { id: group.id, name: group.name, tasks: [], updatedAt: group.updatedAt })
    }
    for (const task of tasks) {
      const id = task.groupId || 'grp_legacy'
      const current = grouped.get(id) ?? { id, name: task.groupName || '历史任务', tasks: [], updatedAt: task.updatedAt }
      current.tasks.push(task)
      if (new Date(task.updatedAt) > new Date(current.updatedAt)) current.updatedAt = task.updatedAt
      grouped.set(id, current)
    }
    return Array.from(grouped.values()).sort((left, right) => +new Date(right.updatedAt) - +new Date(left.updatedAt))
  }, [taskGroups, tasks])

  // refreshTasks reloads lightweight cards without changing the active application tab.
  const refreshTasks = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setLoadError(null)
    try {
      const [items, groupItems] = await Promise.all([listTasks(signal), listTaskGroups(signal)])
      setTasks(items)
      setTaskGroups(groupItems)
      setSelectedGroupId((current) => current && groupItems.some((group) => group.id === current) ? current : groupItems[0]?.id ?? null)
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setLoadError(error instanceof Error ? error.message : '无法连接本地服务')
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void refreshTasks(controller.signal)
    return () => controller.abort()
  }, [refreshTasks])

  // Register the common new-task shortcut without overriding focused form input.
  useEffect(() => {
    const handleShortcut = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'n') {
        event.preventDefault()
        setSubmitError(null)
        setDialogOpen(true)
      }
    }
    window.addEventListener('keydown', handleShortcut)
    return () => window.removeEventListener('keydown', handleShortcut)
  }, [])

  // openSection activates an existing management tab or appends a new one.
  function openSection(section: SectionKey) {
    const id = `section:${section}`
    setTabs((current) => current.some((tab) => tab.id === id) ? current : [...current, { id, kind: 'section', section, label: sectionLabels[section] }])
    setActiveTabId(id)
  }

  // openTask gives every task its own stable tab, matching browser-tab navigation semantics.
  function openTask(taskId: string) {
    const task = tasks.find((item) => item.id === taskId)
    const id = `task:${taskId}`
    setSelectedTaskId(taskId)
    setTabs((current) => current.some((tab) => tab.id === id) ? current : [...current, { id, kind: 'task', taskId, label: task?.name ?? taskId }])
    setActiveTabId(id)
    setRecentTaskIds((current) => {
      const next = [taskId, ...current.filter((id) => id !== taskId)].slice(0, 6)
      window.localStorage.setItem(RECENT_TASKS_STORAGE_KEY, JSON.stringify(next))
      return next
    })
  }

  // closeTab removes one optional tab and activates its left neighbor when necessary.
  function closeTab(tabId: string) {
    setTabs((current) => {
      const index = current.findIndex((tab) => tab.id === tabId)
      if (index < 0 || current.length === 1) return current
      const next = current.filter((tab) => tab.id !== tabId)
      if (activeTabId === tabId) setActiveTabId(next[Math.max(0, index - 1)].id)
      return next
    })
  }

  // selectGroup returns to the task dashboard; individual tasks are opened only from the right pane.
  function selectGroup(groupId: string) {
    setSelectedGroupId(groupId)
    setSelectedTaskId(null)
    openSection('tasks')
  }

  // submitTask creates files and the task-builder plan, then opens the team-building stage.
  // Execution does not advance to intent alignment until the user confirms the team.
  async function submitTask(input: NewTaskRequest) {
    setSubmitting(true)
    setSubmitError(null)
    try {
      const { files, ...metadata } = input
      const created = await createTask(metadata)
      for (const file of files) await uploadTaskFile(created.id, file)
      await generateTaskPlan(created.id)
      setTasks((current) => [created, ...current.filter((item) => item.id !== created.id)])
      setTaskGroups((current) => current.some((group) => group.id === created.groupId)
        ? current
        : [{ id: created.groupId, name: created.groupName, createdAt: created.createdAt, updatedAt: created.updatedAt }, ...current])
      setSelectedGroupId(created.groupId)
      setSelectedTaskId(created.id)
      setDialogOpen(false)
      const taskTabId = `task:${created.id}`
      setTabs((current) => current.some((tab) => tab.id === taskTabId) ? current : [...current, { id: taskTabId, kind: 'task', taskId: created.id, label: created.name }])
      setActiveTabId(taskTabId)
      setRecentTaskIds((current) => {
        const next = [created.id, ...current.filter((id) => id !== created.id)].slice(0, 6)
        window.localStorage.setItem(RECENT_TASKS_STORAGE_KEY, JSON.stringify(next))
        return next
      })
      void refreshTasks()
    } catch (error) {
      setSubmitError(error instanceof Error ? error.message : '任务创建失败')
    } finally {
      setSubmitting(false)
    }
  }

  // removeTask confirms the irreversible operation, then removes every mounted
  // reference so a deleted workspace cannot remain visible through an old tab.
  async function removeTask(taskId: string) {
    const task = tasks.find((item) => item.id === taskId)
    const accepted = await appDialog.confirm({
      title: '删除任务',
      message: `确定删除任务“${task?.name ?? taskId}”吗？\n\n该任务的全部文件、日志和后台进程都会被永久删除，无法撤回。`,
      confirmText: '删除任务',
      tone: 'danger',
    })
    if (!accepted) return
    setDeletingTaskId(taskId)
    setLoadError(null)
    try {
      await deleteTask(taskId)
      setTasks((current) => current.filter((item) => item.id !== taskId))
      setRecentTaskIds((current) => {
        const next = current.filter((id) => id !== taskId)
        window.localStorage.setItem(RECENT_TASKS_STORAGE_KEY, JSON.stringify(next))
        return next
      })
      setSelectedTaskId((current) => current === taskId ? null : current)
      setTabs((current) => current.filter((tab) => tab.taskId !== taskId))
      if (activeTabId === `task:${taskId}`) setActiveTabId('section:tasks')
      await refreshTasks()
    } catch (deleteError) {
      setLoadError(deleteError instanceof Error ? deleteError.message : '任务删除失败')
    } finally {
      setDeletingTaskId(null)
    }
  }

  // renderTab resolves one mounted page. Inactive pages stay mounted so task phase,
  // conversation session, scroll position, and catalog selection survive tab switching.
  function renderTab(tab: AppTab) {
    if (tab.kind === 'task' && tab.taskId) {
      return <TaskWorkspace taskId={tab.taskId} active={tab.id === activeTabId} onBack={() => openSection('tasks')} onTaskChanged={() => void refreshTasks()} />
    }
    if (tab.section === 'tasks') {
      return <TaskHome groups={groups} selectedGroupId={selectedGroupId} selectedTask={selectedTask} loading={loading} error={loadError} deletingTaskId={deletingTaskId} onSelect={setSelectedTaskId} onOpen={openTask} onDelete={(taskId) => void removeTask(taskId)} onNewTask={() => setDialogOpen(true)} />
    }
    if (tab.section === 'agents') return <AssetPage kind="agents" title="智能体" />
    if (tab.section === 'skills') return <AssetPage kind="skills" title="技能" />
    if (tab.section === 'rules') return <AssetPage kind="rules" title="规则" />
    if (tab.section === 'configurations') return <ConfigurationPage />
    return <AssetPage kind="knowledge-bases" title="知识库" />
  }

  return (
    <div className="app-frame">
      <AppSidebar
        activeSection={activeSection}
        groups={groups}
        recentTasks={recentTasks}
        selectedTaskId={selectedTaskId}
        selectedGroupId={selectedGroupId}
        onSectionChange={openSection}
        onGroupSelect={selectGroup}
        onTaskOpen={openTask}
        onNewTask={() => { setSubmitError(null); setDialogOpen(true) }}
        onSettings={() => void appDialog.alert({ title: '本地运行设置', message: '当前使用本地文件模式。服务地址与数据目录可通过 CC_HARDNESS_* 环境变量配置。' })}
        onWorkspaceInfo={() => setWorkspaceInfoOpen(true)}
      />
      <section className="app-content tabbed-content">
        <nav className="app-tab-strip" aria-label="打开的页面">
          {tabs.map((tab) => <button className={tab.id === activeTabId ? 'active' : ''} type="button" key={tab.id} onClick={() => setActiveTabId(tab.id)}><span>{tab.label}</span>{tab.id !== 'section:tasks' && <i role="button" aria-label={`关闭${tab.label}`} onClick={(event) => { event.stopPropagation(); closeTab(tab.id) }}><X size={12} /></i>}</button>)}
        </nav>
        <div className="app-tab-page">{tabs.map((tab) => <div className={`mounted-tab-page ${tab.id === activeTabId ? 'active' : ''}`} key={tab.id}>{renderTab(tab)}</div>)}</div>
      </section>
      <NewTaskDialog open={dialogOpen} submitting={submitting} error={submitError} groups={groups} onClose={() => !submitting && setDialogOpen(false)} onSubmit={submitTask} />
      <WorkspaceInfoDialog open={workspaceInfoOpen} onClose={() => setWorkspaceInfoOpen(false)} />
    </div>
  )
}
