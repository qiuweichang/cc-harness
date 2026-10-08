import {
  Activity, ArrowLeft, Bot, Check, ChevronRight, CirclePause, CirclePlay, FolderArchive,
  ExternalLink, GitBranch, Paperclip, RefreshCw, Rocket, Send, X,
} from 'lucide-react'
import {
  useEffect, useMemo, useRef, useState, type ChangeEvent, type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react'
import {
  acceptTask, advanceTaskStage, closeTask, deployTask, getTask, listAssets,
  listConfigurationSets, listRuntimeSessions, listSessionEvents, mutateTaskPlan, runNode, sendTaskMessage, setTaskLifecycle, startTask,
  uploadTaskFile,
} from '../api'
import type { Asset, ConfigurationSet, DeployTaskInput, RuntimeEvent, RuntimeSession, TaskDetail, TaskPlan, TaskPlanMutation, TeamMember } from '../types'
import { useAppDialog } from './AppDialogProvider'
import { TaskAgentContextDialog } from './TaskAgentContextDialog'
import { TaskCompletionSummary } from './TaskCompletionSummary'
import { TaskConversationHistory } from './TaskConversationHistory'
import { TaskDeploymentDialog } from './TaskDeploymentDialog'
import { TaskPlanCanvas } from './TaskPlanCanvas'
import { TaskStageFilesDialog } from './TaskStageFilesDialog'

interface TaskWorkspaceProps {
  taskId: string
  active: boolean
  onBack: () => void
  onTaskChanged: () => void
}

interface StageDefinition {
  key: string
  label: string
  phases: string[]
  description: string
}

const teamStage: StageDefinition = { key: 'TEAM', label: '团队组建', phases: ['PLANNING'], description: '团队组建智能体根据项目资料选择成员、能力和成员间依赖。' }
const knownStages: Record<string, Omit<StageDefinition, 'phases'>> = {
  ALIGNMENT: { key: 'ALIGNMENT', label: '意图对齐', description: '产品经理完善需求、业务边界和验收标准。' },
  ARCHITECTURE: { key: 'ARCHITECTURE', label: '技术方案确认', description: '架构师优化技术选型、接口、数据库和架构文档。' },
  PROTOTYPE: { key: 'PROTOTYPE', label: 'UI 设计', description: 'UI 设计师生成原型并根据用户意见持续迭代。' },
  DEVELOPMENT: { key: 'DEVELOPMENT', label: '项目开发', description: '项目经理协调前后端开发，并处理开发期增量需求。' },
  INTEGRATION: { key: 'INTEGRATION', label: '集成联调', description: '项目经理协调跨端契约、服务启动和完整业务链路。' },
  QUALITY: { key: 'QUALITY', label: '质量检测', description: '质量检测成员在开发完成后执行专项质量检查。' },
  TEST: { key: 'TEST', label: '测试修复', description: '测试工程师执行 API 与真实页面测试，项目经理路由缺陷。' },
  DELIVERY: { key: 'DELIVERY', label: '交付验收', description: '启动服务并展示交付结果，用户确认后任务才完成。' },
}

interface RemovedPlanSnapshot {
  memberName: string
  plan: TaskPlan
}

type WorkspaceInspector = 'agents' | 'files' | null

// TaskWorkspace presents one current phase, a member-centric DAG, and the
// conversation required by that phase. Diagnostics and artifacts open as overlays.
export function TaskWorkspace({ taskId, active, onBack, onTaskChanged }: TaskWorkspaceProps) {
  const appDialog = useAppDialog()
  const [detail, setDetail] = useState<TaskDetail | null>(null)
  const [planDraft, setPlanDraft] = useState<TaskPlan | null>(null)
  const [agents, setAgents] = useState<Asset[]>([])
  const [skills, setSkills] = useState<Asset[]>([])
  const [rules, setRules] = useState<Asset[]>([])
  const [knowledge, setKnowledge] = useState<Asset[]>([])
  const [configurationSets, setConfigurationSets] = useState<ConfigurationSet[]>([])
  const [inspector, setInspector] = useState<WorkspaceInspector>(null)
  const [message, setMessage] = useState('')
  const [session, setSession] = useState<RuntimeSession | null>(null)
  const [events, setEvents] = useState<RuntimeEvent[]>([])
  const [removedSnapshot, setRemovedSnapshot] = useState<RemovedPlanSnapshot | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  // agentResponding locks the composer after a visible user turn and drives the
  // immediate three-dot activity indicator until the runtime emits a terminal result.
  const [agentResponding, setAgentResponding] = useState(false)
  const [closingTask, setClosingTask] = useState(false)
  // Deployment state is independent from Claude sessions: completed tasks can
  // start generated services while the rest of the workspace remains read-only.
  const [deploymentDialogOpen, setDeploymentDialogOpen] = useState(false)
  const [deployingTask, setDeployingTask] = useState(false)
  const [deploymentError, setDeploymentError] = useState<string | null>(null)
  const [rebuildingWorkflow, setRebuildingWorkflow] = useState(false)
  // teamGraphCollapsed defaults post-planning stages to a compact summary because
  // the active specialist conversation is the primary workspace at that point.
  const [teamGraphCollapsed, setTeamGraphCollapsed] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const pendingPlanRef = useRef<TaskPlan | null>(null)
  // currentEventSequenceRef tracks the active process cursor without rebuilding
  // its polling interval after every event.
  const currentEventSequenceRef = useRef(0)
  // A task or phase entry starts a fresh conversation process on the first user
  // message while TaskConversationHistory continues to show archived sessions.
  const restartConversationRef = useRef(true)
  // activeConversationEntryRef suppresses React StrictMode's repeated effect setup
  // but is cleared when the user leaves the task tab so re-entry restarts the agent.
  const activeConversationEntryRef = useRef('')

  const stageDefinitions = useMemo(() => buildStageDefinitions(planDraft), [planDraft?.workflow])
  const currentStageIndex = detail ? resolveStageIndex(detail, stageDefinitions) : 0
  const currentStage = stageDefinitions[currentStageIndex] ?? teamStage
  const companionId = detail ? resolveCompanionId(detail) : 'task-builder'
  const companion = agents.find((agent) => agent.id === companionId)
  const companionName = companionId.startsWith('task-builder') ? '团队组建智能体' : companion?.name ?? companionId
  const companionSessionId = detail?.runtime?.activeSessions?.[companionId] ?? ''
  const runningCompanionNodeKey = detail?.plan?.workflow.find((node) => (
    node.assigneeId === companionId && detail.runtime?.nodeStates?.[node.key] === 'RUNNING'
  ))?.key ?? ''
  const chatEnabled = Boolean(detail && (detail.status === 'READY' || detail.status === 'PLANNING' || detail.status === 'RUNNING' || detail.status === 'ACCEPTANCE'))
  const conversationEnabled = chatEnabled
  const canSendMessage = conversationEnabled && !agentResponding
  const readOnly = detail?.status === 'CLOSED' || detail?.status === 'COMPLETED'
  const completed = detail?.status === 'COMPLETED'
  const deliveryReview = currentStage.key === 'DELIVERY' && !completed
  // The current phase cannot be archived until each of its agent nodes has
  // produced a durable terminal result. Parallel development work started during
  // prototype does not block prototype approval because it belongs to a later phase.
  const currentPhaseAgentStates = detail?.plan?.workflow
    .filter((node) => node.phase === detail.phase && node.type === 'AGENT')
    .map((node) => detail.runtime?.nodeStates?.[node.key] ?? 'PENDING') ?? []
  const currentPhaseAgentActive = currentPhaseAgentStates.some((status) => ['PENDING', 'READY', 'RUNNING'].includes(status))
  const currentPhaseAgentFailed = currentPhaseAgentStates.some((status) => status === 'FAILED')
  const stageTransitionBlocked = currentPhaseAgentActive || currentPhaseAgentFailed

  // Every non-team phase starts with the member graph collapsed. Returning to
  // team building restores the full editable canvas automatically.
  useEffect(() => {
    setTeamGraphCollapsed(currentStage.key !== 'TEAM')
  }, [currentStage.key])

  // loadWorkspace refreshes task state and all catalogs required by the member dialog.
  async function loadWorkspace() {
    setLoading(true)
    setError(null)
    try {
      const [taskDetail, agentItems, skillItems, ruleItems, knowledgeItems, configurationItems] = await Promise.all([
        getTask(taskId), listAssets('agents'), listAssets('skills'), listAssets('rules'), listAssets('knowledge-bases'), listConfigurationSets(),
      ])
      setDetail(taskDetail)
      const plan = taskDetail.plan ? structuredClone(taskDetail.plan) : null
      setPlanDraft(plan)
      pendingPlanRef.current = plan
      setAgents(agentItems.filter((item) => item.enabled))
      setSkills(skillItems.filter((item) => item.enabled))
      setRules(ruleItems.filter((item) => item.enabled))
      setKnowledge(knowledgeItems.filter((item) => item.enabled))
      setConfigurationSets(configurationItems)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '无法加载任务')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void loadWorkspace() }, [taskId])

  // Reset task-scoped rebuild state when switching tasks.
  useEffect(() => {
    setRebuildingWorkflow(false)
  }, [taskId])

  // Re-entering a task never starts a hidden model turn. It reconnects to an
  // existing process when available; otherwise the first real user message starts
  // a fresh process from durable files, so opening the workspace remains instant.
  useEffect(() => {
    if (!active || !detail || !chatEnabled) {
      activeConversationEntryRef.current = ''
      if (!active || detail?.status === 'CLOSED') setAgentResponding(false)
      return
    }
    const entryIdentity = `${taskId}:${detail.phase}:${companionId}:${companionSessionId || 'lazy'}`
    if (activeConversationEntryRef.current === entryIdentity) return
    activeConversationEntryRef.current = entryIdentity
    restartConversationRef.current = true
    setAgentResponding(false)
    setSession(null)
    setEvents([])
    let cancelled = false
    // resumeInterruptedNode is used only after the backend process table was
    // lost while durable DAG state still says RUNNING. It resumes work in the
    // background without adding a private chat message or locking page entry.
    const resumeInterruptedNode = async () => {
      if (!runningCompanionNodeKey) return
      try {
        const resumed = await runNode(taskId, runningCompanionNodeKey)
        if (cancelled || !('id' in resumed)) return
        restartConversationRef.current = false
        setSession(resumed)
        setAgentResponding(resumed.busy)
      } catch {
        // The composer remains usable. A real user message can still start a
        // replacement session and the backend diagnostics retain the failure.
      }
    }
    if (!companionSessionId) {
      void resumeInterruptedNode()
      return () => { cancelled = true }
    }
    // Query the live collection once instead of requesting a possibly stale
    // persisted session ID. Backend restarts intentionally discard process
    // pipes, so a missing old ID is normal and must not surface as HTTP 400.
    void listRuntimeSessions().then((runtimeSessions) => {
      if (cancelled) return
      const activeSession = runtimeSessions.find((item) => item.id === companionSessionId)
      if (!activeSession || activeSession.status !== 'RUNNING') {
        void resumeInterruptedNode()
        return
      }
      restartConversationRef.current = false
      setSession(activeSession)
      setAgentResponding(activeSession.busy)
    }).catch(() => {
      void resumeInterruptedNode()
    })
    return () => { cancelled = true }
  }, [active, chatEnabled, companionId, companionSessionId, detail?.id, detail?.phase, runningCompanionNodeKey, taskId])

  // Poll ordered events only for the phase companion shown in the main conversation.
  useEffect(() => {
    if (!session) return
    currentEventSequenceRef.current = 0
    setEvents([])
    let cancelled = false
    const poll = async () => {
      try {
        const incoming = await listSessionEvents(session.id, currentEventSequenceRef.current)
        if (!cancelled && incoming.length) {
          currentEventSequenceRef.current = incoming[incoming.length - 1].sequence
          setEvents((current) => [...current, ...incoming])
          if (incoming.some(agentTurnFinished)) {
            setAgentResponding(false)
            void refreshWorkspaceAfterAgentTurn()
          }
        }
      } catch (pollError) {
        if (!cancelled) {
          setAgentResponding(false)
          setError(pollError instanceof Error ? pollError.message : '会话事件读取失败')
        }
      }
    }
    void poll()
    const timer = window.setInterval(() => void poll(), 250)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [session?.id])

  // During team building, refresh plan revisions written by the task-builder API.
  useEffect(() => {
    if (!detail || currentStage.key !== 'TEAM') return
    const timer = window.setInterval(async () => {
      try {
        const latest = await getTask(taskId)
        if (latest.plan && latest.plan.revision > (pendingPlanRef.current?.revision ?? 0)) {
          const plan = structuredClone(latest.plan)
          pendingPlanRef.current = plan
          setPlanDraft(plan)
          setDetail(latest)
        }
      } catch {
        // The primary load and message actions surface actionable errors; background refresh stays quiet.
      }
    }, 3000)
    return () => window.clearInterval(timer)
  }, [currentStage.key, detail?.id, taskId])

  // Post-team stages refresh phase state and semantic DAG revisions. PLANNING is
  // included because intent, architecture, and prototype stages intentionally keep
  // that lifecycle status while users may still add or remove team members.
  useEffect(() => {
    if (!detail || currentStage.key === 'TEAM' || !['PLANNING', 'RUNNING', 'ACCEPTANCE'].includes(detail.status)) return
    const timer = window.setInterval(async () => {
      try {
        const latest = await getTask(taskId)
        const definitions = buildStageDefinitions(latest.plan ?? null)
        const previousIndex = resolveStageIndex(detail, definitions)
        const nextIndex = resolveStageIndex(latest, definitions)
        setDetail(latest)
        if (latest.plan && latest.plan.revision > (pendingPlanRef.current?.revision ?? 0)) {
          const plan = structuredClone(latest.plan)
          pendingPlanRef.current = plan
          setPlanDraft(plan)
        }
        if (nextIndex !== previousIndex) setSession(null)
      } catch {
        // Keep the last durable view while the local service is temporarily unavailable.
      }
    }, 4000)
    return () => window.clearInterval(timer)
  }, [currentStage.key, detail?.id, detail?.phase, detail?.status, taskId])

  // refreshWorkspaceAfterAgentTurn replaces the local plan with the durable file
  // after every agent result, including plan mutations performed through the API.
  async function refreshWorkspaceAfterAgentTurn() {
    try {
      const latest = await getTask(taskId)
      const latestPlan = latest.plan ? structuredClone(latest.plan) : null
      setDetail(latest)
      setPlanDraft(latestPlan)
      pendingPlanRef.current = latestPlan
      onTaskChanged()
    } catch (refreshError) {
      setError(refreshError instanceof Error ? refreshError.message : '智能体完成后刷新任务失败')
    }
  }

  // sendConversationInstruction dispatches one visible user turn. The task
  // builder's local skill requires plan-related requests to call the backend API.
  async function sendConversationInstruction(text: string): Promise<boolean> {
    if (!text.trim() || !conversationEnabled || agentResponding) return false
    setError(null)
    setAgentResponding(true)
    try {
      const activeSession = await sendTaskMessage(taskId, companionId, text.trim(), restartConversationRef.current)
      restartConversationRef.current = false
      setSession(activeSession)
      return true
    } catch (sendError) {
      setAgentResponding(false)
      setError(sendError instanceof Error ? sendError.message : '消息发送失败')
      return false
    }
  }

  // applyManualPlanMutation routes every direct team edit through the serialized
  // backend API and replaces the canvas with the validated response.
  async function applyManualPlanMutation(mutation: TaskPlanMutation, failureMessage: string): Promise<TaskPlan | null> {
    if (rebuildingWorkflow) return null
    setRebuildingWorkflow(true)
    setError(null)
    try {
      const saved = await mutateTaskPlan(taskId, mutation)
      const next = structuredClone(saved)
      pendingPlanRef.current = next
      setPlanDraft(next)
      setDetail((current) => current ? { ...current, plan: saved } : current)
      onTaskChanged()
      return saved
    } catch (mutationError) {
      setError(mutationError instanceof Error ? mutationError.message : failureMessage)
      return null
    } finally {
      setRebuildingWorkflow(false)
    }
  }

  // addMember asks the backend to resolve catalog defaults and construct a safe
  // role-aware workflow branch without waiting for a model turn.
  async function addMember(agentId: string) {
    if (!planDraft || rebuildingWorkflow || agentResponding) return
    await applyManualPlanMutation({ action: 'ADD_MEMBER', agentId }, '成员新增失败')
  }

  // updateMember persists version, skill, rule, and knowledge changes through the
  // member-specific backend mutation instead of rewriting the plan in the browser.
  async function updateMember(memberId: string, update: Partial<TeamMember>) {
    if (!planDraft || rebuildingWorkflow || agentResponding) return
    const member = planDraft.team.find((item) => item.id === memberId)
    if (!member) return
    await applyManualPlanMutation({ action: 'UPDATE_MEMBER', memberId, member: { ...member, ...update } }, '成员能力更新失败')
  }

  // removeMember confirms the destructive team edit and lets the backend bypass
  // removed nodes before atomically validating the repaired DAG.
  async function removeMember(memberId: string) {
    if (!planDraft || rebuildingWorkflow || agentResponding) return
    const member = planDraft.team.find((item) => item.id === memberId)
    if (!member || member.required) return
    const accepted = await appDialog.confirm({
      title: '移除团队成员',
      message: `确认移除“${member.name}”吗？移除后系统将重新构建完整 DAG。`,
      confirmText: '确认移除',
      tone: 'danger',
    })
    if (!accepted) return
    const snapshot = { memberName: member.name, plan: structuredClone(planDraft) }
    const saved = await applyManualPlanMutation({ action: 'REMOVE_MEMBER', memberId }, '成员移除失败')
    if (saved) setRemovedSnapshot(snapshot)
  }

  // undoRemove restores the exact member binding through ADD_MEMBER; the backend
  // creates a fresh safe workflow branch against the latest plan.
  async function undoRemove() {
    if (!removedSnapshot || rebuildingWorkflow || agentResponding) return
    const accepted = await appDialog.confirm({
      title: '撤回成员移除',
      message: `确认恢复“${removedSnapshot.memberName}”吗？恢复后智能体将再次重构完整 DAG。`,
      confirmText: '确认恢复',
    })
    if (!accepted) return
    const snapshot = removedSnapshot
    const previousMember = snapshot.plan.team.find((member) => member.name === snapshot.memberName)
    if (!previousMember) return
    const saved = await applyManualPlanMutation({ action: 'ADD_MEMBER', member: previousMember }, '成员恢复失败')
    if (saved) setRemovedSnapshot(null)
  }

  // toggleTaskResource persists task-wide capabilities through one backend mutation.
  async function toggleTaskResource(field: 'taskRuleIds' | 'taskKnowledgeIds', id: string) {
    if (!planDraft || rebuildingWorkflow) return
    const current = planDraft[field]
    const next = current.includes(id) ? current.filter((item) => item !== id) : [...current, id]
    await applyManualPlanMutation({
      action: 'UPDATE_TASK_RESOURCES',
      taskRuleIds: field === 'taskRuleIds' ? next : planDraft.taskRuleIds,
      taskKnowledgeIds: field === 'taskKnowledgeIds' ? next : planDraft.taskKnowledgeIds,
    }, '任务能力更新失败')
  }

  // toggleTaskConfigurationSet binds reusable environment values through the
  // same revisioned task-resource mutation used by team-building agents.
  async function toggleTaskConfigurationSet(id: string) {
    if (!planDraft || rebuildingWorkflow) return
    const next = planDraft.configSetIds.includes(id)
      ? planDraft.configSetIds.filter((item) => item !== id)
      : [...planDraft.configSetIds, id]
    await applyManualPlanMutation({
      action: 'UPDATE_TASK_RESOURCES',
      taskRuleIds: planDraft.taskRuleIds,
      taskKnowledgeIds: planDraft.taskKnowledgeIds,
      configSetIds: next,
    }, '任务配置集更新失败')
  }

  // confirmTeam advances the already persisted backend plan into intent alignment.
  async function confirmTeam() {
    setBusy(true)
    try {
      const result = await startTask(taskId)
      const nextCompanionId = resolveCompanionId(result.task)
      activeConversationEntryRef.current = `${taskId}:${result.task.phase}:${nextCompanionId}:${result.session.id}`
      restartConversationRef.current = false
      setAgentResponding(result.session.busy)
      setDetail(result.task)
      setPlanDraft(result.task.plan ? structuredClone(result.task.plan) : null)
      setSession(result.session)
      setEvents([])
      onTaskChanged()
    } catch (startError) {
      setError(startError instanceof Error ? startError.message : '任务启动失败')
    } finally {
      setBusy(false)
    }
  }

  // submitMessage routes conversation to the role mandated by the architecture document.
  async function submitMessage(event: FormEvent) {
    event.preventDefault()
    const text = message.trim()
    if (!text || !canSendMessage) return
    if (
      (currentStage.key === 'ALIGNMENT' || currentStage.key === 'ARCHITECTURE') &&
      !stageTransitionBlocked &&
      isStageApprovalMessage(text)
    ) {
      setMessage('')
      await advanceCurrentStage()
      return
    }
    setMessage('')
    const sent = await sendConversationInstruction(text)
    if (!sent) setMessage(text)
  }

  // handleMessageKeyDown sends a composed message with Enter while preserving
  // Shift+Enter for multiline input and IME composition for Chinese text.
  function handleMessageKeyDown(event: ReactKeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) return
    event.preventDefault()
    if (canSendMessage && message.trim()) event.currentTarget.form?.requestSubmit()
  }

  // closeCurrentTask confirms the terminal transition, stops every managed
  // process, and keeps the loaded workspace visible in read-only mode.
  async function closeCurrentTask() {
    const accepted = await appDialog.confirm({
      title: '关闭任务',
      message: '确定关闭当前任务吗？\n\n所有后台进程都会停止，任务将变为“已关闭”且只能查看。',
      confirmText: '关闭任务',
      tone: 'danger',
    })
    if (!accepted) return
    setClosingTask(true)
    setError(null)
    try {
      const updated = await closeTask(taskId)
      setDetail((current) => current ? { ...current, ...updated } : current)
      setSession(null)
      setEvents([])
      setAgentResponding(false)
      activeConversationEntryRef.current = ''
      onTaskChanged()
    } catch (closeError) {
      setError(closeError instanceof Error ? closeError.message : '任务关闭失败')
    } finally {
      setClosingTask(false)
    }
  }

  // advanceCurrentStage treats the user's click as approval, archives this phase,
  // and lets the backend start ready work in the next dynamic phase.
  async function advanceCurrentStage() {
    setBusy(true)
    try {
      const latest = await advanceTaskStage(taskId)
      const plan = latest.plan ? structuredClone(latest.plan) : null
      const nextCompanionId = resolveCompanionId(latest)
      const activeSessionId = latest.runtime?.activeSessions?.[nextCompanionId]
      let automaticSession: RuntimeSession | null = null
      if (activeSessionId) {
        try {
          const runtimeSessions = await listRuntimeSessions()
          automaticSession = runtimeSessions.find((item) => item.id === activeSessionId) ?? null
        } catch {
          // The entry effect can recover any running DAG node; failure to read
          // monitoring metadata must not block stage advancement.
        }
      }
      if (automaticSession?.status === 'RUNNING') {
        activeConversationEntryRef.current = `${taskId}:${latest.phase}:${nextCompanionId}:${automaticSession.id}`
        restartConversationRef.current = false
        setAgentResponding(automaticSession.busy)
      }
      setDetail(latest)
      setPlanDraft(plan)
      pendingPlanRef.current = plan
      setSession(automaticSession)
      setEvents([])
      onTaskChanged()
    } catch (advanceError) {
      setError(advanceError instanceof Error ? advanceError.message : '进入下一阶段失败')
    } finally {
      setBusy(false)
    }
  }

  // uploadFile adds phase context material without restoring the removed Files sub-page.
  async function uploadFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    try {
      await uploadTaskFile(taskId, file)
    } catch (uploadError) {
      setError(uploadError instanceof Error ? uploadError.message : '文件上传失败')
    }
  }

  // changeLifecycle pauses or resumes the background team while retaining the current phase.
  async function changeLifecycle(action: 'pause' | 'resume') {
    try {
      const updated = await setTaskLifecycle(taskId, action)
      setDetail((current) => current ? { ...current, ...updated } : current)
      onTaskChanged()
    } catch (actionError) {
      setError(actionError instanceof Error ? actionError.message : '状态更新失败')
    }
  }

  // completeAcceptance records the explicit final user confirmation.
  async function completeAcceptance() {
    const accepted = await appDialog.confirm({
      title: '确认交付',
      message: '确认所有工作结果和交付文档均满足要求吗？\n\n确认后任务将完成，只能在工作区查看汇总结果。',
      confirmText: '确认交付',
    })
    if (!accepted) return
    try {
      await acceptTask(taskId)
      const latest = await getTask(taskId)
      const plan = latest.plan ? structuredClone(latest.plan) : null
      setDetail(latest)
      setPlanDraft(plan)
      pendingPlanRef.current = plan
      onTaskChanged()
    } catch (acceptanceError) {
      setError(acceptanceError instanceof Error ? acceptanceError.message : '任务验收失败')
    }
  }

  // deployCompletedTask delegates port allocation and process ownership to the
  // backend, then updates the persisted addresses without reloading the workspace.
  async function deployCompletedTask(input: DeployTaskInput) {
    setDeployingTask(true)
    setDeploymentError(null)
    try {
      const delivery = await deployTask(taskId, input)
      setDetail((current) => current ? { ...current, delivery } : current)
      setDeploymentDialogOpen(false)
      onTaskChanged()
    } catch (deploymentFailure) {
      setDeploymentError(deploymentFailure instanceof Error ? deploymentFailure.message : '部署失败')
    } finally {
      setDeployingTask(false)
    }
  }

  if (loading || !detail) return <div className="workspace-loading">正在读取任务阶段…</div>

  return (
    <div className="task-workspace phase-workspace">
      <header className="workspace-topbar">
        <button className="icon-button" type="button" onClick={onBack} aria-label="返回任务组"><ArrowLeft size={18} /></button>
        <div className="workspace-task-heading">
          <h1>{detail.name}</h1>
          {!completed && <span className={`workspace-status status-${detail.status.toLowerCase()}`}>{workspaceStatusLabel(detail.status)}</span>}
        </div>
        <button className="button secondary compact workspace-tool-button" type="button" onClick={() => setInspector('agents')}><Activity size={14} />后台智能体</button>
        <button className="button secondary compact workspace-tool-button" type="button" onClick={() => setInspector('files')}><FolderArchive size={14} />阶段文件</button>
        {completed && detail.delivery?.status === 'RUNNING' && <>
          <a className="deployment-address-button" href={detail.delivery.frontendUrl} target="_blank" rel="noreferrer">前端 {detail.delivery.frontendUrl}<ExternalLink size={12} /></a>
          <a className="deployment-address-button" href={detail.delivery.backendUrl} target="_blank" rel="noreferrer">后端 {detail.delivery.backendUrl}<ExternalLink size={12} /></a>
          <button className="button secondary compact" type="button" onClick={() => { setDeploymentError(null); setDeploymentDialogOpen(true) }}><RefreshCw size={14} />重新部署</button>
        </>}
        {completed && detail.delivery?.status !== 'RUNNING' && <button className="button primary compact" type="button" onClick={() => { setDeploymentError(null); setDeploymentDialogOpen(true) }}><Rocket size={14} />部署</button>}
        {detail.status === 'RUNNING' && <button className="button secondary compact" type="button" onClick={() => void changeLifecycle('pause')}><CirclePause size={15} />暂停</button>}
        {detail.status === 'PAUSED' && <button className="button secondary compact" type="button" onClick={() => void changeLifecycle('resume')}><CirclePlay size={15} />恢复</button>}
        {detail.status === 'CLOSED' && <span className="closed-readonly-label">仅查看</span>}
        {!readOnly && <button className="button danger-button compact workspace-close-button" type="button" disabled={closingTask} onClick={() => void closeCurrentTask()}><X size={15} />{closingTask ? '关闭中…' : '关闭任务'}</button>}
      </header>

      {error && <div className="page-error workspace-error"><span>{error}</span><button type="button" onClick={() => setError(null)}><X size={14} /></button></div>}

      <main className="phase-workspace-body">
        <header className="current-stage-header">
          <div><span className="eyebrow">{completed ? 'TASK COMPLETE' : 'CURRENT PHASE'}</span><h2>{completed ? '任务完成' : currentStage.label}</h2></div>
          {currentStage.key === 'TEAM' && !readOnly && <button className="button primary" type="button" disabled={busy || rebuildingWorkflow || agentResponding || !planDraft} onClick={() => void confirmTeam()}><Check size={15} />{busy || rebuildingWorkflow || agentResponding ? '处理中…' : '下一步'}</button>}
          {currentStage.key === 'DELIVERY' && detail.status === 'ACCEPTANCE' && <button className="button primary" type="button" onClick={() => void completeAcceptance()}><Check size={15} />确认交付</button>}
          {currentStage.key !== 'TEAM' && detail.status !== 'ACCEPTANCE' && detail.status !== 'COMPLETED' && !readOnly && <button className="button primary" type="button" disabled={busy || rebuildingWorkflow || agentResponding || stageTransitionBlocked} onClick={() => void advanceCurrentStage()}><Check size={15} />{currentPhaseAgentFailed ? '阶段异常' : busy || rebuildingWorkflow || agentResponding || currentPhaseAgentActive ? '处理中…' : '下一步'}</button>}
        </header>

        {(completed || deliveryReview) && <TaskCompletionSummary detail={detail} stages={stageDefinitions} mode={completed ? 'completed' : 'delivery'} onOpenFiles={() => setInspector('files')} />}

        {!completed && !deliveryReview && planDraft && <>
          {currentStage.key !== 'TEAM' && teamGraphCollapsed ? (
            <button className="collapsed-team-graph" type="button" onClick={() => setTeamGraphCollapsed(false)} aria-expanded="false">
              <span><GitBranch size={15} /><strong>团队成员与工作流</strong><small>{planDraft.team.length} 名成员</small></span>
              <span>展开查看<ChevronRight size={14} /></span>
            </button>
          ) : (
            <TaskPlanCanvas plan={planDraft} agents={agents} skills={skills} rules={rules} knowledge={knowledge} nodeStates={detail.runtime?.nodeStates} showStatus={currentStage.key !== 'TEAM'} rebuilding={rebuildingWorkflow} readOnly={readOnly} allowTeamEditing={currentStage.key === 'TEAM' && !readOnly} onAddMember={addMember} onUpdateMember={updateMember} onRemoveMember={removeMember} removedMemberName={removedSnapshot?.memberName} onUndoRemove={undoRemove} onToggleCollapse={currentStage.key === 'TEAM' ? undefined : () => setTeamGraphCollapsed(true)} />
          )}
          {currentStage.key === 'TEAM' && <section className="task-scope-resources"><div><span>任务规则</span>{rules.map((rule) => <button className={planDraft.taskRuleIds.includes(rule.id) ? 'selected' : ''} type="button" disabled={rebuildingWorkflow || readOnly} key={rule.id} onClick={() => toggleTaskResource('taskRuleIds', rule.id)}>{rule.name}</button>)}</div><div><span>任务知识库</span>{knowledge.length ? knowledge.map((item) => <button className={planDraft.taskKnowledgeIds.includes(item.id) ? 'selected' : ''} type="button" disabled={rebuildingWorkflow || readOnly} key={item.id} onClick={() => toggleTaskResource('taskKnowledgeIds', item.id)}>{item.name}</button>) : <small>暂无已启用知识库</small>}</div><div><span>配置集</span>{configurationSets.length ? configurationSets.map((set) => <button className={planDraft.configSetIds.includes(set.id) ? 'selected' : ''} type="button" disabled={rebuildingWorkflow || readOnly} key={set.id} onClick={() => toggleTaskConfigurationSet(set.id)} title={set.entries.map((entry) => entry.name).join('、')}>{set.name}</button>) : <small>暂无配置集，可前往配置管理新增</small>}</div></section>}
        </>}

        {!completed && <section className="phase-conversation">
          <header><span className="canvas-avatar"><Bot size={17} /></span><div><strong>与{companionName}对话</strong></div></header>
          <div className="phase-chat-events">
            <TaskConversationHistory taskId={taskId} memberId={companionId} agentName={companionName} activeSession={session} liveEvents={events} />
            {agentResponding && (
              <div className="agent-typing-message" role="status" aria-label="智能体正在运行">
                <strong>{companionName}正在思考</strong><span aria-hidden="true"><i /><i /><i /></span>
              </div>
            )}
          </div>
          <form className="phase-chat-input" onSubmit={submitMessage}><button className="icon-button" type="button" disabled={!canSendMessage} onClick={() => fileInputRef.current?.click()} title="上传阶段资料"><Paperclip size={18} /></button><textarea value={message} disabled={!canSendMessage} onKeyDown={handleMessageKeyDown} onChange={(event) => setMessage(event.target.value)} placeholder={readOnly ? '任务已关闭，仅允许查看' : agentResponding ? '智能体正在运行…' : detail.status === 'PAUSED' ? '任务已暂停' : `发送给${companionName}，通过对话调整当前阶段…`} /><button className="send-button" type="submit" disabled={!message.trim() || !canSendMessage}><Send size={17} /></button></form>
        </section>}
      </main>
      <input ref={fileInputRef} className="hidden-input" type="file" onChange={(event) => void uploadFile(event)} />
      {inspector === 'agents' && <TaskAgentContextDialog taskId={taskId} plan={planDraft} onClose={() => setInspector(null)} />}
      {inspector === 'files' && <TaskStageFilesDialog taskId={taskId} rootPath={detail.rootPath} stages={stageDefinitions} onClose={() => setInspector(null)} />}
      <TaskDeploymentDialog open={deploymentDialogOpen} deployment={detail.delivery} deploying={deployingTask} error={deploymentError} onClose={() => !deployingTask && setDeploymentDialogOpen(false)} onDeploy={(input) => void deployCompletedTask(input)} />
    </div>
  )
}

// agentTurnFinished recognizes the terminal Claude/process event for a visible
// or automatically dispatched turn while keeping the process reusable afterward.
function agentTurnFinished(event: RuntimeEvent): boolean {
  if (event.type === 'process_exit') return true
  if (event.type !== 'claude_event' || !event.payload || typeof event.payload !== 'object') return false
  return String((event.payload as Record<string, unknown>).type ?? '').toLowerCase() === 'result'
}

// workspaceStatusLabel keeps lifecycle chrome readable without exposing internal
// English state identifiers in the task detail header.
function workspaceStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    DRAFT: '待规划',
    PLANNING: '规划中',
    READY: '待启动',
    RUNNING: '运行中',
    PAUSED: '已暂停',
    ACCEPTANCE: '待验收',
    COMPLETED: '已完成',
    CLOSED: '已关闭',
  }
  return labels[status] ?? status
}

// buildStageDefinitions derives stage order from the workflow so task-builder-created
// phases appear naturally. Team building is always first and DEVELOPMENT is retained.
function buildStageDefinitions(plan: TaskPlan | null): StageDefinition[] {
  const phases: string[] = []
  for (const node of plan?.workflow ?? []) {
    if (node.phase && !phases.includes(node.phase)) phases.push(node.phase)
  }
  if (!phases.includes('DEVELOPMENT')) {
    const deliveryIndex = phases.indexOf('DELIVERY')
    phases.splice(deliveryIndex >= 0 ? deliveryIndex : phases.length, 0, 'DEVELOPMENT')
  }
  return [
    teamStage,
    ...phases.map((phase) => {
      const known = knownStages[phase]
      if (known) return { ...known, phases: [phase] }
      const label = phase.split(/[-_]/).filter(Boolean).map((part) => part.slice(0, 1).toUpperCase() + part.slice(1).toLowerCase()).join(' ')
      return { key: phase, label: label || '动态阶段', phases: [phase], description: '由团队组建智能体根据新增成员职责生成的动态执行阶段。' }
    }),
  ]
}

// resolveStageIndex maps task metadata to the current dynamic stage.
function resolveStageIndex(detail: TaskDetail, definitions: StageDefinition[]): number {
  if (detail.status === 'DRAFT' || detail.phase === 'PLANNING') return 0
  const index = definitions.findIndex((stage) => stage.phases.includes(detail.phase))
  return index >= 0 ? index : detail.status === 'COMPLETED' ? definitions.length - 1 : 1
}

// resolveCompanionId follows the document's direct-conversation routing rules.
function resolveCompanionId(detail: TaskDetail): string {
  if (detail.phase === 'PLANNING') return 'task-builder'
  if (detail.phase === 'ALIGNMENT') return 'product-manager'
  if (detail.phase === 'ARCHITECTURE') return 'architect'
  if (detail.phase === 'PROTOTYPE') return 'ui-designer'
  return 'project-manager'
}

// isStageApprovalMessage recognizes only explicit, short approval replies. More
// descriptive messages still go to the active specialist for normal discussion.
function isStageApprovalMessage(value: string): boolean {
  const normalized = value.replace(/[。！!，,\s]/g, '')
  return new Set([
    '不需要修改', '无需修改', '没有修改', '没问题', '确认', '可以', '通过',
    '进入下一阶段', '下一步', '不需要了',
  ]).has(normalized)
}
