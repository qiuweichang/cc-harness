import { Activity, AlertTriangle, Bot, Brain, LoaderCircle, MessageCircle, Settings2, Wrench, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { listTaskSessions, listTaskSessionEvents } from '../api'
import type { RuntimeEvent, RuntimeSession, TaskPlan } from '../types'
import { presentRuntimeEvents, type RuntimePresentationKind } from './runtimePresentation'

interface TaskAgentContextDialogProps {
  taskId: string
  plan: TaskPlan | null
  onClose: () => void
}

interface AgentSessionGroup {
  id: string
  name: string
  status: string
  latestAt: string
  sessions: RuntimeSession[]
}

// TaskAgentContextDialog groups every restart of the same task member into one
// diagnostic timeline while retaining the underlying live and archived sessions.
export function TaskAgentContextDialog({ taskId, plan, onClose }: TaskAgentContextDialogProps) {
  const [sessions, setSessions] = useState<RuntimeSession[]>([])
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null)
  const [eventsBySession, setEventsBySession] = useState<Record<string, RuntimeEvent[]>>({})
  const [loadingSessions, setLoadingSessions] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // sequenceBySessionRef keeps one independent polling cursor per restart because
  // sequence values are monotonic only inside a single Claude process.
  const sequenceBySessionRef = useRef<Record<string, number>>({})
  const agentGroups = useMemo(() => groupSessionsByAgent(sessions, plan), [plan, sessions])
  const selectedGroup = agentGroups.find((group) => group.id === selectedAgentId) ?? null
  const selectedSessionIds = selectedGroup?.sessions.map((session) => session.id).join('|') ?? ''
  const presentations = useMemo(() => {
    const merged = (selectedGroup?.sessions ?? [])
      .flatMap((session) => eventsBySession[session.id] ?? [])
      .sort((left, right) => Date.parse(left.createdAt) - Date.parse(right.createdAt))
      .map((event, index) => ({ ...event, sequence: index + 1 }))
    return presentRuntimeEvents(merged)
  }, [eventsBySession, selectedGroup])

  // Refresh session metadata so newly restarted agents join their existing group
  // without requiring the diagnostic dialog to be reopened.
  useEffect(() => {
    let cancelled = false
    const refreshSessions = async () => {
      try {
        const items = (await listTaskSessions(taskId))
          .sort((left, right) => Date.parse(right.startedAt) - Date.parse(left.startedAt))
        if (cancelled) return
        const groups = groupSessionsByAgent(items, plan)
        setSessions(items)
        setSelectedAgentId((current) => groups.some((group) => group.id === current) ? current : groups[0]?.id ?? null)
        setError(null)
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '无法读取后台智能体')
      } finally {
        if (!cancelled) setLoadingSessions(false)
      }
    }
    void refreshSessions()
    const timer = window.setInterval(() => void refreshSessions(), 2000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [plan, taskId])

  // Poll every restart belonging to the selected agent in parallel, then render a
  // single chronological history. This avoids serial network waterfalls for agents
  // with many archived sessions.
  useEffect(() => {
    setEventsBySession({})
    sequenceBySessionRef.current = {}
    if (!selectedGroup || !selectedSessionIds) return
    const groupedSessions = selectedGroup.sessions
    let cancelled = false
    const refreshEvents = async () => {
      try {
        const results = await Promise.all(groupedSessions.map(async (session) => ({
          sessionId: session.id,
          events: await listTaskSessionEvents(taskId, session.id, sequenceBySessionRef.current[session.id] ?? 0),
        })))
        if (cancelled) return
        setEventsBySession((current) => {
          const next = { ...current }
          for (const result of results) {
            if (result.events.length === 0) continue
            sequenceBySessionRef.current[result.sessionId] = result.events[result.events.length - 1].sequence
            next[result.sessionId] = [...(next[result.sessionId] ?? []), ...result.events]
          }
          return next
        })
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '无法读取智能体事件')
      }
    }
    void refreshEvents()
    const timer = window.setInterval(() => void refreshEvents(), 1000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [selectedAgentId, selectedSessionIds, taskId])

  return (
    <div className="dialog-backdrop task-inspector-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="dialog-card task-inspector-dialog" role="dialog" aria-modal="true" aria-labelledby="agent-context-title">
        <header className="task-inspector-header">
          <div><span className="eyebrow">BACKGROUND AGENTS</span><h2 id="agent-context-title">后台智能体上下文</h2></div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭后台智能体上下文"><X size={18} /></button>
        </header>
        {error && <div className="page-error inspector-error"><span>{error}</span><button type="button" onClick={() => setError(null)}><X size={13} /></button></div>}
        <div className="agent-context-layout">
          <aside className="agent-context-sessions">
            <div><strong>全部智能体</strong><span>{agentGroups.length}</span></div>
            {loadingSessions ? <div className="inspector-loading"><LoaderCircle size={18} />读取中…</div> : agentGroups.length === 0 ? <div className="inspector-empty">没有找到该任务的智能体记录。</div> : agentGroups.map((group) => (
              <button className={group.id === selectedAgentId ? 'active' : ''} type="button" key={group.id} onClick={() => setSelectedAgentId(group.id)}>
                <span className="canvas-avatar"><Bot size={15} /></span>
                <div><strong>{group.name}</strong><small>{group.sessions.length} 次会话</small><time>{formatSessionTime(group.latestAt)}</time></div>
                <em className={`session-status status-${group.status.toLowerCase()}`}>{sessionStatusLabel(group.status)}</em>
              </button>
            ))}
          </aside>
          <section className="agent-context-events">
            <header>
              <div><Activity size={15} /><strong>{selectedGroup?.name ?? '请选择智能体'}</strong></div>
              {selectedGroup && <span className="agent-session-count">{selectedGroup.sessions.length} 次会话已合并</span>}
            </header>
            <div>
              {!selectedGroup ? <div className="inspector-empty">从左侧选择一个智能体。</div> : presentations.length === 0 ? <div className="inspector-loading"><LoaderCircle size={18} />等待上下文事件…</div> : presentations.map((entry) => (
                <article className={`agent-context-event context-${entry.kind}`} key={entry.id}>
                  <header><span className="context-event-category">{presentationIcon(entry.kind)}<b>{entry.label}</b></span><time>{formatSessionTime(entry.createdAt)}</time></header>
                  <p>{entry.text}</p>
                </article>
              ))}
            </div>
          </section>
        </div>
      </section>
    </div>
  )
}

// groupSessionsByAgent normalizes special builder routes and calculates one
// aggregate status so repeated restarts never create duplicate sidebar records.
function groupSessionsByAgent(sessions: RuntimeSession[], plan: TaskPlan | null): AgentSessionGroup[] {
  const grouped = new Map<string, RuntimeSession[]>()
  for (const session of sessions) {
    const id = normalizeAgentId(session.memberId)
    grouped.set(id, [...(grouped.get(id) ?? []), session])
  }
  return Array.from(grouped, ([id, items]) => {
    const ordered = [...items].sort((left, right) => Date.parse(left.startedAt) - Date.parse(right.startedAt))
    const latest = ordered[ordered.length - 1]
    return {
      id,
      name: sessionMemberName(id, plan),
      status: aggregateSessionStatus(ordered),
      latestAt: latest.startedAt,
      sessions: ordered,
    }
  }).sort((left, right) => Date.parse(right.latestAt) - Date.parse(left.latestAt))
}

// normalizeAgentId collapses team-builder planning and rebuild routes into the
// same logical agent while preserving ordinary team member identifiers.
function normalizeAgentId(memberId: string): string {
  if (memberId.startsWith('task-builder')) return 'task-builder'
  return memberId.startsWith('chat:') ? memberId.slice('chat:'.length) : memberId
}

// aggregateSessionStatus prioritizes active and abnormal states over archived
// records so the grouped row communicates what the agent is doing now.
function aggregateSessionStatus(sessions: RuntimeSession[]): string {
  const priority = ['RUNNING', 'STARTING', 'FAILED', 'STOPPED', 'EXITED', 'ARCHIVED']
  return priority.find((status) => sessions.some((session) => session.status === status)) ?? sessions.at(-1)?.status ?? 'ARCHIVED'
}

// presentationIcon assigns a distinct visual metaphor to user prompts, model
// reasoning, tool traffic, normal answers, system events, and failures.
function presentationIcon(kind: RuntimePresentationKind): ReactNode {
  if (kind === 'user') return <MessageCircle size={13} />
  if (kind === 'thinking') return <Brain size={13} />
  if (kind === 'tool') return <Wrench size={13} />
  if (kind === 'assistant') return <Bot size={13} />
  if (kind === 'error') return <AlertTriangle size={13} />
  return <Settings2 size={13} />
}

// sessionMemberName maps internal member routes to their current user-facing name.
function sessionMemberName(memberId: string, plan: TaskPlan | null): string {
  if (memberId === 'task-builder') return '团队组建智能体'
  return plan?.team.find((member) => member.id === memberId)?.name ?? memberId
}

// sessionStatusLabel translates runtime lifecycle values for the diagnostic list.
function sessionStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    STARTING: '启动中',
    RUNNING: '运行中',
    EXITED: '已结束',
    ARCHIVED: '已归档',
    STOPPED: '已停止',
    FAILED: '异常',
  }
  return labels[status] ?? status
}

// formatSessionTime renders compact local timestamps for dense diagnostic lists.
function formatSessionTime(value: string): string {
  return new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
