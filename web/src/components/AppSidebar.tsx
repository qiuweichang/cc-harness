import {
  BookOpen,
  Bot,
  Boxes,
  ChevronDown,
  Clock3,
  FileClock,
  Plus,
  Scale,
  Settings,
  SlidersHorizontal,
  Sparkles,
} from 'lucide-react'
import { useState } from 'react'
import type { SectionKey, Task, TaskGroup } from '../types'

interface AppSidebarProps {
  activeSection: SectionKey
  groups: TaskGroup[]
  recentTasks: Task[]
  selectedTaskId: string | null
  selectedGroupId: string | null
  onSectionChange: (section: SectionKey) => void
  onGroupSelect: (groupId: string) => void
  onTaskOpen: (taskId: string) => void
  onNewTask: () => void
  onSettings: () => void
  onWorkspaceInfo: () => void
}

const navigation = [
  { key: 'agents' as const, label: '智能体', icon: Bot },
  { key: 'skills' as const, label: '技能', icon: Sparkles },
  { key: 'rules' as const, label: '规则', icon: Scale },
  { key: 'knowledge' as const, label: '知识库', icon: BookOpen },
  { key: 'configurations' as const, label: '配置管理', icon: SlidersHorizontal },
]

// AppSidebar provides Codex-style primary navigation and recent task selection.
export function AppSidebar({
  activeSection,
  groups,
  recentTasks,
  selectedTaskId,
  selectedGroupId,
  onSectionChange,
  onGroupSelect,
  onTaskOpen,
  onNewTask,
  onSettings,
  onWorkspaceInfo,
}: AppSidebarProps) {
  const [taskGroupsOpen, setTaskGroupsOpen] = useState(true)
  // toggleTaskGroups keeps the task dashboard active while revealing its group tree.
  function toggleTaskGroups() {
    onSectionChange('tasks')
    setTaskGroupsOpen((current) => !current)
  }

  return (
    <aside className="app-sidebar">
      <div className="sidebar-brand-row">
        <button className="brand-button" type="button" onClick={() => onSectionChange('tasks')}>
          <span className="brand-mark"><Boxes size={16} strokeWidth={2.2} /></span>
          <span>cc-hardness</span>
          <ChevronDown size={14} className="brand-chevron" />
        </button>
      </div>

      <button className="new-task-button" type="button" onClick={onNewTask}>
        <Plus size={17} />
        <span>新建任务</span>
        <kbd>Ctrl N</kbd>
      </button>

      <nav className="primary-nav" aria-label="主要导航">
        <button
          className={`nav-item ${activeSection === 'tasks' ? 'active' : ''}`}
          type="button"
          onClick={toggleTaskGroups}
        >
          <FileClock size={17} />
          <span>任务</span>
          <ChevronDown size={13} className={`nav-chevron ${taskGroupsOpen ? 'open' : ''}`} />
        </button>
        {taskGroupsOpen && <div className="task-group-tree">
          {groups.length === 0 ? <p className="sidebar-empty">暂无任务组</p> : groups.map((group) => (
            <div className="sidebar-task-group" key={group.id}>
              <button className={`task-group-button ${selectedGroupId === group.id && activeSection === 'tasks' ? 'active' : ''}`} type="button" onClick={() => onGroupSelect(group.id)}>
                <span>{group.name}</span><small>{group.tasks.length}</small>
              </button>
            </div>
          ))}
        </div>}
        {navigation.map((item) => {
          const Icon = item.icon
          return (
            <button
              className={`nav-item ${activeSection === item.key ? 'active' : ''}`}
              key={item.key}
              type="button"
              onClick={() => onSectionChange(item.key)}
            >
              <Icon size={17} />
              <span>{item.label}</span>
            </button>
          )
        })}
        <div className="recent-task-section">
          <div className="sidebar-section-label"><Clock3 size={12} /><span>最近打开</span></div>
          {recentTasks.length === 0 ? <p className="sidebar-empty">暂无最近任务</p> : recentTasks.map((task) => (
            <button className={`recent-task-button ${selectedTaskId === task.id ? 'active' : ''}`} type="button" key={task.id} onClick={() => onTaskOpen(task.id)}>
              <span>{task.name}</span>
              <small>{recentTaskStatusLabel(task.status)}</small>
            </button>
          ))}
        </div>
      </nav>

      <div className="sidebar-flex-spacer" />

      <div className="sidebar-footer">
        <button className="nav-item" type="button" onClick={onSettings}>
          <Settings size={17} />
          <span>设置</span>
        </button>
        <button className="profile-row" type="button" onClick={onWorkspaceInfo}>
          <span className="profile-avatar">CC</span>
          <div>
            <strong>本地工作区</strong>
            <span>文件模式</span>
          </div>
          <span className="local-indicator" title="本地服务已连接" />
        </button>
      </div>
    </aside>
  )
}

// recentTaskStatusLabel keeps the compact sidebar readable without leaking
// internal lifecycle identifiers into persistent navigation.
function recentTaskStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    DRAFT: '待规划',
    PLANNING: '规划中',
    READY: '待启动',
    RUNNING: '进行中',
    PAUSED: '已暂停',
    ACCEPTANCE: '待验收',
    COMPLETED: '已完成',
    CLOSED: '已关闭',
  }
  return labels[status] ?? status
}
