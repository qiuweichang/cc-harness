import { Bot, ChevronUp, GitBranch, LoaderCircle, Maximize2, Plus, RotateCcw, Scale, Sparkles, Trash2, X } from 'lucide-react'
import {
  useEffect, useMemo, useRef, useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import type { Asset, TaskPlan, TeamMember, WorkflowNode } from '../types'

interface TaskPlanCanvasProps {
  plan: TaskPlan
  agents: Asset[]
  skills: Asset[]
  rules: Asset[]
  knowledge: Asset[]
  nodeStates?: Record<string, string>
  showStatus: boolean
  rebuilding: boolean
  readOnly: boolean
  // allowTeamEditing restricts structural and capability mutations to the
  // team-building stage while cards remain draggable in later stages.
  allowTeamEditing: boolean
  onAddMember: (agentId: string) => void
  onUpdateMember: (memberId: string, update: Partial<TeamMember>) => void
  onRemoveMember: (memberId: string) => void
  removedMemberName?: string
  onUndoRemove: () => void
  onToggleCollapse?: () => void
}

interface MemberEdge {
  source: string
  target: string
}

interface Point {
  x: number
  y: number
}

interface MemberLayout {
  points: Record<string, Point>
  width: number
  height: number
}

interface CanvasViewport {
  x: number
  y: number
  scale: number
}

interface MemberDragState {
  pointerId: number
  memberId: string
  offsetX: number
  offsetY: number
  startClientX: number
  startClientY: number
  suppressClick: boolean
}

const memberWidth = 182
const memberHeight = 126

// TaskPlanCanvas treats members as the only visible DAG nodes. Workflow definitions remain
// the execution source of truth, but each definition is displayed inside its assignee's task list.
export function TaskPlanCanvas({
  plan, agents, skills, rules, knowledge, nodeStates, showStatus, rebuilding, readOnly, allowTeamEditing,
  onAddMember, onUpdateMember, onRemoveMember, removedMemberName, onUndoRemove, onToggleCollapse,
}: TaskPlanCanvasProps) {
  const [selectedMemberId, setSelectedMemberId] = useState<string | null>(null)
  const [positions, setPositions] = useState<Record<string, Point>>({})
  const [viewport, setViewport] = useState<CanvasViewport>({ x: 18, y: 18, scale: 1 })
  const [panning, setPanning] = useState(false)
  // memberDragRef tracks transient pointer coordinates without rerendering for
  // drag bookkeeping; only the visible member position enters React state.
  const memberDragRef = useRef<MemberDragState>({
    pointerId: -1, memberId: '', offsetX: 0, offsetY: 0,
    startClientX: 0, startClientY: 0, suppressClick: false,
  })
  const panRef = useRef({ pointerId: -1, x: 0, y: 0, originX: 0, originY: 0 })
  const viewportRef = useRef<HTMLDivElement>(null)
  // viewportStateRef lets the native non-passive wheel listener read the latest
  // transform without being reattached after every zoom step.
  const viewportStateRef = useRef(viewport)
  viewportStateRef.current = viewport
  const selectedMember = plan.team.find((member) => member.id === selectedMemberId) ?? null
  const selectedAgent = agents.find((agent) => agent.id === selectedMember?.agentId)
  const edges = useMemo(() => deriveMemberEdges(plan.workflow), [plan.workflow])
  const automaticLayout = useMemo(() => computeMemberLayout(plan.team, edges, plan.workflow), [plan.team, edges, plan.workflow])
  const layoutSignature = useMemo(
    () => `${plan.team.map((member) => member.id).join('|')}::${edges.map((edge) => `${edge.source}>${edge.target}`).sort().join('|')}`,
    [plan.team, edges],
  )
  const memberPoints = new Map(plan.team.map((member) => [member.id, positions[member.id] ?? automaticLayout.points[member.id]]))
  const viewportHeight = Math.min(620, Math.max(330, automaticLayout.height + 36))

  // A semantic DAG change resets manual card positions to the new layered layout.
  // Capability/version edits do not change the signature, so user placement is preserved.
  useEffect(() => {
    setPositions(automaticLayout.points)
  }, [layoutSignature])

  // fitWorkflow keeps the complete team visible and centers small teams without
  // preventing the user from zooming or panning afterward.
  function fitWorkflow() {
    const host = viewportRef.current
    if (!host) return
    const rect = host.getBoundingClientRect()
    const scale = Math.max(.35, Math.min(1.15, (rect.width - 36) / automaticLayout.width, (rect.height - 36) / automaticLayout.height))
    setViewport({
      x: (rect.width - automaticLayout.width * scale) / 2,
      y: (rect.height - automaticLayout.height * scale) / 2,
      scale,
    })
  }

  // Refit after structural changes and container resizes so every generated member
  // remains visible even when the workspace width changes.
  useEffect(() => {
    const host = viewportRef.current
    if (!host) return
    const frame = window.requestAnimationFrame(fitWorkflow)
    const observer = new ResizeObserver(() => fitWorkflow())
    observer.observe(host)
    return () => {
      window.cancelAnimationFrame(frame)
      observer.disconnect()
    }
  }, [layoutSignature, automaticLayout.width, automaticLayout.height])

  // React delegates wheel handlers as passive in some browser configurations.
  // A native non-passive listener is required so zooming the DAG never scrolls
  // the surrounding task page at the same time.
  useEffect(() => {
    const host = viewportRef.current
    if (!host) return
    const handleWheel = (event: WheelEvent) => zoomCanvas(event)
    host.addEventListener('wheel', handleWheel, { passive: false })
    return () => host.removeEventListener('wheel', handleWheel)
  }, [])

  // startMemberDrag records a world-space pointer offset so dragging remains
  // accurate at every zoom level and does not depend on native HTML5 drag/drop.
  function startMemberDrag(event: ReactPointerEvent<HTMLButtonElement>) {
    if (event.button !== 0 || rebuilding || readOnly) return
    event.stopPropagation()
    const memberId = event.currentTarget.dataset.memberId ?? ''
    const host = viewportRef.current
    const point = memberPoints.get(memberId)
    if (!memberId || !host || !point) return
    const rect = host.getBoundingClientRect()
    const current = viewportStateRef.current
    const worldX = (event.clientX - rect.left - current.x) / current.scale
    const worldY = (event.clientY - rect.top - current.y) / current.scale
    memberDragRef.current = {
      pointerId: event.pointerId,
      memberId,
      offsetX: worldX - point.x,
      offsetY: worldY - point.y,
      startClientX: event.clientX,
      startClientY: event.clientY,
      suppressClick: false,
    }
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  // moveMemberDrag stores unbounded scene coordinates. Panning is the navigation
  // boundary, so members can be placed above, below, or beside the auto layout.
  function moveMemberDrag(event: ReactPointerEvent<HTMLButtonElement>) {
    const drag = memberDragRef.current
    if (drag.pointerId !== event.pointerId || !drag.memberId) return
    event.stopPropagation()
    const host = viewportRef.current
    if (!host) return
    const rect = host.getBoundingClientRect()
    const current = viewportStateRef.current
    const x = (event.clientX - rect.left - current.x) / current.scale - drag.offsetX
    const y = (event.clientY - rect.top - current.y) / current.scale - drag.offsetY
    if (Math.abs(event.clientX - drag.startClientX) > 3 || Math.abs(event.clientY - drag.startClientY) > 3) {
      drag.suppressClick = true
    }
    setPositions((current) => ({
      ...current,
      [drag.memberId]: { x, y },
    }))
  }

  // stopMemberDrag releases pointer capture while retaining one click-suppression
  // flag so dropping a card never opens its detail dialog accidentally.
  function stopMemberDrag(event: ReactPointerEvent<HTMLButtonElement>) {
    if (memberDragRef.current.pointerId !== event.pointerId) return
    event.stopPropagation()
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    memberDragRef.current.pointerId = -1
    memberDragRef.current.memberId = ''
  }

  // selectMember opens details only for a true click, not the pointerup generated
  // at the end of a member reposition operation.
  function selectMember(memberId: string) {
    if (memberDragRef.current.suppressClick) {
      memberDragRef.current.suppressClick = false
      return
    }
    setSelectedMemberId(memberId)
  }

  // startCanvasPan reserves the secondary mouse button for viewport navigation.
  function startCanvasPan(event: ReactPointerEvent<HTMLDivElement>) {
    if (event.button !== 2) return
    event.preventDefault()
    panRef.current = {
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      originX: viewport.x,
      originY: viewport.y,
    }
    event.currentTarget.setPointerCapture(event.pointerId)
    setPanning(true)
  }

  // moveCanvasPan updates translation only for the captured secondary-button pointer.
  function moveCanvasPan(event: ReactPointerEvent<HTMLDivElement>) {
    if (!panning || panRef.current.pointerId !== event.pointerId) return
    setViewport((current) => ({
      ...current,
      x: panRef.current.originX + event.clientX - panRef.current.x,
      y: panRef.current.originY + event.clientY - panRef.current.y,
    }))
  }

  // stopCanvasPan releases navigation state after the secondary button is released.
  function stopCanvasPan(event: ReactPointerEvent<HTMLDivElement>) {
    if (panRef.current.pointerId !== event.pointerId) return
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    panRef.current.pointerId = -1
    setPanning(false)
  }

  // zoomCanvas keeps the world point under the cursor fixed while clamping zoom
  // to a range that remains useful for both overview and detail inspection.
  function zoomCanvas(event: WheelEvent) {
    event.preventDefault()
    event.stopPropagation()
    const host = viewportRef.current
    if (!host) return
    const current = viewportStateRef.current
    const rect = host.getBoundingClientRect()
    const pointerX = event.clientX - rect.left
    const pointerY = event.clientY - rect.top
    const factor = event.deltaY < 0 ? 1.1 : .9
    const nextScale = Math.max(.35, Math.min(2, current.scale * factor))
    const worldX = (pointerX - current.x) / current.scale
    const worldY = (pointerY - current.y) / current.scale
    setViewport({
      x: pointerX - worldX * nextScale,
      y: pointerY - worldY * nextScale,
      scale: nextScale,
    })
  }

  // tasksForMember returns the complete phase-ordered checklist owned by one member.
  function tasksForMember(memberId: string): WorkflowNode[] {
    return plan.workflow.filter((node) => node.assigneeId === memberId)
  }

  // memberState aggregates task states so a member card communicates progress without workflow nodes.
  function memberState(memberId: string): string {
    const states = tasksForMember(memberId).map((node) => nodeStates?.[node.key] ?? node.status)
    if (states.some((state) => state === 'FAILED')) return 'FAILED'
    if (states.some((state) => state === 'RUNNING' || state === 'WAITING_USER')) return 'RUNNING'
    if (states.length > 0 && states.every((state) => state === 'SUCCEEDED' || state === 'SKIPPED')) return 'SUCCEEDED'
    if (states.some((state) => state === 'READY')) return 'READY'
    return 'PENDING'
  }

  // toggleCapability updates one immutable capability list through the parent auto-save flow.
  function toggleCapability(member: TeamMember, field: 'skillIds' | 'ruleIds' | 'knowledgeIds', id: string) {
    if (!allowTeamEditing) return
    const current = member[field]
    const next = current.includes(id) ? current.filter((item) => item !== id) : [...current, id]
    if (field === 'skillIds') onUpdateMember(member.id, { skillIds: next })
    if (field === 'ruleIds') onUpdateMember(member.id, { ruleIds: next })
    if (field === 'knowledgeIds') onUpdateMember(member.id, { knowledgeIds: next })
  }

  // toggleToolbarFromKeyboard gives the collapsible title bar the same behavior
  // for keyboard users as a pointer click without intercepting toolbar controls.
  function toggleToolbarFromKeyboard(event: ReactKeyboardEvent<HTMLElement>) {
    if (!onToggleCollapse || (event.key !== 'Enter' && event.key !== ' ')) return
    event.preventDefault()
    onToggleCollapse()
  }

  return (
    <section className="member-dag-shell">
      <header
        className={`member-dag-toolbar ${onToggleCollapse ? 'is-collapsible' : ''}`}
        role={onToggleCollapse ? 'button' : undefined}
        tabIndex={onToggleCollapse ? 0 : undefined}
        aria-expanded={onToggleCollapse ? true : undefined}
        onClick={onToggleCollapse}
        onKeyDown={toggleToolbarFromKeyboard}
      >
        <div><GitBranch size={16} /><span>团队成员与工作流</span>{onToggleCollapse && <ChevronUp className="member-dag-toggle-icon" size={14} />}</div>
        <aside onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
          <button className="fit-workflow-button" type="button" onClick={fitWorkflow} title="显示全部成员"><Maximize2 size={13} />适应画布</button>
          {allowTeamEditing && removedMemberName && <button className="undo-member-button" type="button" disabled={rebuilding} onClick={onUndoRemove}><RotateCcw size={13} />撤回移除：{removedMemberName}</button>}
          {allowTeamEditing && <label className={rebuilding ? 'disabled' : ''}><Plus size={14} /><select defaultValue="" disabled={rebuilding} onChange={(event) => { if (event.target.value) onAddMember(event.target.value); event.target.value = '' }}><option value="">添加成员…</option>{agents.map((agent) => <option value={agent.id} key={agent.id}>{agent.name} · {agent.role}</option>)}</select></label>}
        </aside>
      </header>

      <div
        ref={viewportRef}
        className={`member-dag-viewport ${panning ? 'panning' : ''}`}
        style={{ height: viewportHeight }}
        onContextMenu={(event) => event.preventDefault()}
        onPointerDown={startCanvasPan}
        onPointerMove={moveCanvasPan}
        onPointerUp={stopCanvasPan}
        onPointerCancel={stopCanvasPan}
      >
        <div className="member-dag-scene" style={{ width: automaticLayout.width, height: automaticLayout.height, transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.scale})` }}>
          <svg width={automaticLayout.width} height={automaticLayout.height} aria-hidden="true">
            <defs><marker id="member-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" /></marker></defs>
            {edges.map((edge) => {
              const source = memberPoints.get(edge.source)
              const target = memberPoints.get(edge.target)
              if (!source || !target) return null
              const outgoing = edges.filter((item) => item.source === edge.source)
              const incoming = edges.filter((item) => item.target === edge.target)
              const sourceIndex = outgoing.findIndex((item) => item.target === edge.target)
              const targetIndex = incoming.findIndex((item) => item.source === edge.source)
              const startX = source.x + memberWidth
              const endX = target.x
              const startY = source.y + memberHeight / 2 + (sourceIndex - (outgoing.length - 1) / 2) * 16
              const endY = target.y + memberHeight / 2 + (targetIndex - (incoming.length - 1) / 2) * 16
              const horizontalDistance = Math.max(72, endX - startX)
              const sourceControlX = startX + horizontalDistance * .42
              const targetControlX = endX - horizontalDistance * .42
              return <path className="member-dag-edge" key={`${edge.source}-${edge.target}`} d={`M ${startX} ${startY} C ${sourceControlX} ${startY}, ${targetControlX} ${endY}, ${endX} ${endY}`} markerEnd="url(#member-arrow)" />
            })}
          </svg>
          {plan.team.map((member) => {
            const point = memberPoints.get(member.id)
            if (!point) return null
            const tasks = tasksForMember(member.id)
            const state = memberState(member.id)
            return (
              <button data-member-id={member.id} className={`member-dag-card state-${state.toLowerCase()}`} style={{ left: point.x, top: point.y, width: memberWidth }} type="button" key={member.id} onPointerDown={startMemberDrag} onPointerMove={moveMemberDrag} onPointerUp={stopMemberDrag} onPointerCancel={stopMemberDrag} onClick={() => selectMember(member.id)}>
                <span className="canvas-avatar"><Bot size={17} /></span>
                <div><strong>{member.name}</strong><small>{member.role}</small></div>
                {showStatus && <em>{state}</em>}
                <p>{tasks.length} 项任务 · v{member.agentVersion}</p>
                <i>{tasks.slice(0, 2).map((task) => task.name).join('、') || '协调与阶段对话'}</i>
              </button>
            )
          })}
        </div>
        <span className="member-dag-zoom">{Math.round(viewport.scale * 100)}%</span>
        {rebuilding && <div className="workflow-rebuilding" role="status"><LoaderCircle size={25} /><strong>AI 正在重新构建工作流</strong><small>团队组建智能体正在检查成员位置、依赖关系和动态阶段…</small></div>}
      </div>

      {selectedMember && <div className="dialog-backdrop member-detail-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && setSelectedMemberId(null)}>
        <section className="dialog-card member-detail-dialog" role="dialog" aria-modal="true">
          <div className="dialog-header"><div><span className="eyebrow">TEAM MEMBER</span><h2>{selectedMember.name}</h2><p>{selectedAgent?.description ?? selectedMember.role}</p></div><button className="icon-button" type="button" onClick={() => setSelectedMemberId(null)}><X size={18} /></button></div>
          <div className="member-dialog-meta">
            <label><span>智能体版本</span><select value={selectedMember.agentVersion} disabled={rebuilding || readOnly || !allowTeamEditing} onChange={(event) => onUpdateMember(selectedMember.id, { agentVersion: event.target.value })}>{(selectedAgent?.versions ?? [selectedMember.agentVersion]).map((version) => <option key={version}>{version}</option>)}</select></label>
            <div><span>角色</span><strong>{selectedMember.role}</strong></div>
            {showStatus && <div><span>成员状态</span><strong>{memberState(selectedMember.id)}</strong></div>}
          </div>
          <section className="member-dialog-section"><h3>任务清单</h3><div className="member-task-list">{tasksForMember(selectedMember.id).map((task) => { const state = nodeStates?.[task.key] ?? task.status; return <article key={task.key}><span className={`node-state state-${state.toLowerCase()}`}>{state}</span><div><strong>{task.name}</strong><p>{task.description}</p><small>{task.phase} · 等待 {task.waitFor.length ? task.waitFor.join('、') : '无'}</small></div></article> })}</div></section>
          <section className="member-dialog-section"><h3><Sparkles size={14} />技能</h3><div className="dialog-capability-grid">{skills.map((skill) => <button className={selectedMember.skillIds.includes(skill.id) ? 'selected' : ''} disabled={rebuilding || readOnly || !allowTeamEditing} type="button" key={skill.id} onClick={() => toggleCapability(selectedMember, 'skillIds', skill.id)}>{skill.name}</button>)}</div></section>
          <section className="member-dialog-section"><h3><Scale size={14} />专属规则</h3><div className="dialog-capability-grid">{rules.map((rule) => <button className={selectedMember.ruleIds.includes(rule.id) ? 'selected' : ''} disabled={rebuilding || readOnly || !allowTeamEditing} type="button" key={rule.id} onClick={() => toggleCapability(selectedMember, 'ruleIds', rule.id)}>{rule.name}</button>)}</div></section>
          {knowledge.length > 0 && <section className="member-dialog-section"><h3>知识库</h3><div className="dialog-capability-grid">{knowledge.map((item) => <button className={selectedMember.knowledgeIds.includes(item.id) ? 'selected' : ''} disabled={rebuilding || readOnly || !allowTeamEditing} type="button" key={item.id} onClick={() => toggleCapability(selectedMember, 'knowledgeIds', item.id)}>{item.name}</button>)}</div></section>}
          <div className="dialog-actions">{allowTeamEditing && !selectedMember.required && <button className="button danger-button" type="button" disabled={rebuilding} onClick={() => { onRemoveMember(selectedMember.id); setSelectedMemberId(null) }}><Trash2 size={14} />移除成员</button>}<button className="button primary" type="button" onClick={() => setSelectedMemberId(null)}>完成</button></div>
        </section>
      </div>}
    </section>
  )
}

// computeMemberLayout assigns topological columns and vertically separates siblings.
// This makes A → B/C render B and C in the same next column, one above the other.
function computeMemberLayout(team: TeamMember[], edges: MemberEdge[], workflow: WorkflowNode[]): MemberLayout {
  const memberIds = new Set(team.map((member) => member.id))
  const adjacency = new Map<string, string[]>(team.map((member) => [member.id, []]))
  const indegree = new Map<string, number>(team.map((member) => [member.id, 0]))
  for (const edge of edges) {
    if (!memberIds.has(edge.source) || !memberIds.has(edge.target)) continue
    adjacency.get(edge.source)?.push(edge.target)
    indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1)
  }

  const firstTaskIndex = new Map<string, number>()
  workflow.forEach((node, index) => {
    if (node.assigneeId && !firstTaskIndex.has(node.assigneeId)) firstTaskIndex.set(node.assigneeId, index)
  })
  // The project manager is the visual orchestration root even when that member
  // owns a later delivery task. Late re-entry must not create test → manager
  // edges that turn an otherwise valid workflow into a cyclic member diagram.
  firstTaskIndex.set('project-manager', -1)
  const sortByWorkflow = (left: string, right: string) => (firstTaskIndex.get(left) ?? Number.MAX_SAFE_INTEGER) - (firstTaskIndex.get(right) ?? Number.MAX_SAFE_INTEGER)
  const queue = team.map((member) => member.id).filter((id) => (indegree.get(id) ?? 0) === 0).sort(sortByWorkflow)
  const levels = new Map<string, number>(queue.map((id) => [id, 0]))

  while (queue.length > 0) {
    const source = queue.shift()!
    for (const target of adjacency.get(source) ?? []) {
      levels.set(target, Math.max(levels.get(target) ?? 0, (levels.get(source) ?? 0) + 1))
      indegree.set(target, (indegree.get(target) ?? 1) - 1)
      if (indegree.get(target) === 0) {
        queue.push(target)
        queue.sort(sortByWorkflow)
      }
    }
  }

  // Any defensive cycle residue is placed after its earliest workflow predecessor;
  // the backend still remains responsible for rejecting cyclic execution plans.
  for (const member of team) {
    if (!levels.has(member.id)) levels.set(member.id, Math.max(0, ...levels.values()) + 1)
  }

  const columns = new Map<number, string[]>()
  for (const member of team) {
    const level = levels.get(member.id) ?? 0
    columns.set(level, [...(columns.get(level) ?? []), member.id])
  }
  for (const members of columns.values()) members.sort(sortByWorkflow)

  const horizontalGap = 112
  const verticalGap = 50
  const paddingX = 56
  const paddingY = 46
  const maxRows = Math.max(1, ...Array.from(columns.values(), (members) => members.length))
  const height = Math.max(250, paddingY * 2 + maxRows * memberHeight + (maxRows - 1) * verticalGap)
  const maxLevel = Math.max(0, ...columns.keys())
  const width = Math.max(360, paddingX * 2 + (maxLevel + 1) * memberWidth + maxLevel * horizontalGap)
  const points: Record<string, Point> = {}

  for (const [level, members] of columns) {
    const columnHeight = members.length * memberHeight + Math.max(0, members.length - 1) * verticalGap
    const startY = (height - columnHeight) / 2
    members.forEach((memberId, index) => {
      points[memberId] = {
        x: paddingX + level * (memberWidth + horizontalGap),
        y: startY + index * (memberHeight + verticalGap),
      }
    })
  }
  return { points, width, height }
}

// deriveMemberEdges collapses workflow-node dependencies into unique assignee-to-assignee
// edges. Approval nodes are transparent, so their nearest upstream assignees remain visible.
function deriveMemberEdges(workflow: WorkflowNode[]): MemberEdge[] {
  const nodes = new Map(workflow.map((node) => [node.key, node]))
  const result = new Map<string, MemberEdge>()
  const firstTaskIndex = new Map<string, number>()
  workflow.forEach((node, index) => {
    if (node.assigneeId && !firstTaskIndex.has(node.assigneeId)) firstTaskIndex.set(node.assigneeId, index)
  })

  // upstreamOwners recursively resolves the members responsible for a dependency chain.
  function upstreamOwners(key: string, visiting = new Set<string>()): string[] {
    if (visiting.has(key)) return []
    visiting.add(key)
    const node = nodes.get(key)
    if (!node) return []
    if (node.assigneeId) return [node.assigneeId]
    return Array.from(new Set(node.waitFor.flatMap((dependency) => upstreamOwners(dependency, new Set(visiting)))))
  }

  for (const node of workflow) {
    if (!node.assigneeId) continue
    for (const dependency of node.waitFor) {
      for (const source of upstreamOwners(dependency)) {
        if (source === node.assigneeId) continue
        if (node.assigneeId === 'project-manager') continue
        // A member may re-enter in a later phase. Keep the first forward precedence
        // edge so the collapsed member graph stays acyclic and readable.
        if ((firstTaskIndex.get(source) ?? 0) > (firstTaskIndex.get(node.assigneeId) ?? Number.MAX_SAFE_INTEGER)) continue
        result.set(`${source}->${node.assigneeId}`, { source, target: node.assigneeId })
      }
    }
  }
  const rootOwners = workflow.filter((node) => node.waitFor.length === 0).map((node) => node.assigneeId).filter((id): id is string => Boolean(id))
  const roots = Array.from(new Set(rootOwners))
  for (const target of roots) {
    if (target !== 'project-manager') result.set(`project-manager->${target}`, { source: 'project-manager', target })
  }
  return reduceTransitiveMemberEdges(Array.from(result.values()))
}

// reduceTransitiveMemberEdges removes visual shortcuts such as A → C when the
// member graph already contains A → B → C. Task-node dependencies remain intact;
// only the collapsed member visualization is simplified after team mutations.
function reduceTransitiveMemberEdges(edges: MemberEdge[]): MemberEdge[] {
  const adjacency = new Map<string, string[]>()
  for (const edge of edges) adjacency.set(edge.source, [...(adjacency.get(edge.source) ?? []), edge.target])

  // hasAlternativePath searches for the same reachability while excluding one
  // candidate direct edge, which is the standard DAG transitive-reduction test.
  function hasAlternativePath(candidate: MemberEdge): boolean {
    const queue = [candidate.source]
    const visited = new Set<string>([candidate.source])
    while (queue.length > 0) {
      const source = queue.shift()!
      for (const target of adjacency.get(source) ?? []) {
        if (source === candidate.source && target === candidate.target) continue
        if (target === candidate.target) return true
        if (!visited.has(target)) {
          visited.add(target)
          queue.push(target)
        }
      }
    }
    return false
  }

  return edges.filter((edge) => !hasAlternativePath(edge))
}
