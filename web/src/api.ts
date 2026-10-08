import type {
  Asset,
  AssetInput,
  AssetKind,
  ChangeRequest,
  ClaudeInfo,
  ConfigurationSet,
  ConfigurationSetInput,
  CreateTaskInput,
  DeployTaskInput,
  DeliveryInfo,
  RuntimeEvent,
  RuntimeSession,
  RuntimeState,
  Task,
  TaskDetail,
  TaskFile,
  TaskGroupSummary,
  TaskPlan,
  TaskPlanMutation,
} from './types'

// ApiError preserves the status and user-facing message returned by the control plane.
export class ApiError extends Error {
  readonly status: number

  /** Creates a typed API failure that UI components can present without parsing response bodies. */
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

// parseResponse decodes a successful JSON response or throws the shared API error shape.
async function parseResponse<T>(response: Response): Promise<T> {
  if (response.status === 204) return undefined as T
  const payload = (await response.json()) as T & { error?: { message?: string } }
  if (!response.ok) throw new ApiError(response.status, payload.error?.message ?? '请求失败，请稍后重试')
  return payload
}

// requestJSON sends a JSON request through the shared response parser.
async function requestJSON<T>(url: string, method: string, body?: unknown): Promise<T> {
  const response = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return parseResponse<T>(response)
}

// getClaudeInfo returns only the non-secret local CLI and model configuration
// required by the workspace information dialog.
export async function getClaudeInfo(): Promise<ClaudeInfo> {
  return requestJSON<ClaudeInfo>('/api/v1/system/claude-info', 'GET')
}

// listTasks scans all file-backed task directories through the local API.
export async function listTasks(signal?: AbortSignal): Promise<Task[]> {
  const response = await fetch('/api/v1/tasks', { signal })
  return (await parseResponse<{ items: Task[] }>(response)).items
}

// listTaskGroups returns durable navigation groups, including empty groups.
export async function listTaskGroups(signal?: AbortSignal): Promise<TaskGroupSummary[]> {
  const response = await fetch('/api/v1/task-groups', { signal })
  return (await parseResponse<{ items: TaskGroupSummary[] }>(response)).items
}

// listConfigurationSets returns reusable middleware and environment collections.
export async function listConfigurationSets(signal?: AbortSignal): Promise<ConfigurationSet[]> {
  const response = await fetch('/api/v1/config-sets', { signal })
  return (await parseResponse<{ items: ConfigurationSet[] }>(response)).items
}

// createConfigurationSet persists one new reusable configuration collection.
export async function createConfigurationSet(input: ConfigurationSetInput): Promise<ConfigurationSet> {
  return requestJSON<ConfigurationSet>('/api/v1/config-sets', 'POST', input)
}

// updateConfigurationSet replaces one existing configuration collection.
export async function updateConfigurationSet(id: string, input: ConfigurationSetInput): Promise<ConfigurationSet> {
  return requestJSON<ConfigurationSet>(`/api/v1/config-sets/${id}`, 'PUT', input)
}

// deleteConfigurationSet removes one collection after backend reference checks.
export async function deleteConfigurationSet(id: string): Promise<void> {
  await requestJSON<void>(`/api/v1/config-sets/${id}`, 'DELETE')
}

// getTask loads task metadata, plan, runtime, and change history.
export async function getTask(id: string): Promise<TaskDetail> {
  return requestJSON<TaskDetail>(`/api/v1/tasks/${id}`, 'GET')
}

// createTask creates a task directory whose root is also the project workspace.
export async function createTask(input: CreateTaskInput): Promise<Task> {
  return requestJSON<Task>('/api/v1/tasks', 'POST', input)
}

// deleteTask stops managed task processes and permanently removes its workspace.
export async function deleteTask(id: string): Promise<void> {
  await requestJSON<void>(`/api/v1/tasks/${id}`, 'DELETE')
}

// closeTask stops managed processes and preserves the workspace as read-only.
export async function closeTask(id: string): Promise<Task> {
  return requestJSON<Task>(`/api/v1/tasks/${id}/close`, 'POST')
}

// generateTaskPlan creates the required team and default DAG.
export async function generateTaskPlan(id: string): Promise<TaskPlan> {
  return requestJSON<TaskPlan>(`/api/v1/tasks/${id}/plan`, 'POST')
}

// saveTaskPlan persists user-edited team and workflow data.
export async function saveTaskPlan(id: string, plan: TaskPlan): Promise<TaskPlan> {
  return requestJSON<TaskPlan>(`/api/v1/tasks/${id}/plan`, 'PUT', plan)
}

// mutateTaskPlan applies one serialized member, capability, resource, or DAG
// operation without asking the client to rewrite the complete plan file.
export async function mutateTaskPlan(id: string, mutation: TaskPlanMutation): Promise<TaskPlan> {
  return requestJSON<TaskPlan>(`/api/v1/tasks/${id}/plan/mutations`, 'POST', mutation)
}

// startTask materializes assets and starts the project-manager session.
export async function startTask(id: string): Promise<{ task: TaskDetail; session: RuntimeSession }> {
  return requestJSON<{ task: TaskDetail; session: RuntimeSession }>(`/api/v1/tasks/${id}/start`, 'POST')
}

// advanceTaskStage archives the current phase and returns the next active phase.
export async function advanceTaskStage(id: string): Promise<TaskDetail> {
  return requestJSON<TaskDetail>(`/api/v1/tasks/${id}/advance`, 'POST')
}

// setTaskLifecycle pauses or resumes task workflow progress.
export async function setTaskLifecycle(id: string, action: 'pause' | 'resume'): Promise<Task> {
  return requestJSON<Task>(`/api/v1/tasks/${id}/${action}`, 'POST')
}

// acceptTask records explicit final delivery acceptance.
export async function acceptTask(id: string): Promise<Task> {
  return requestJSON<Task>(`/api/v1/tasks/${id}/accept`, 'POST')
}

// saveDelivery stores service addresses shown on the final acceptance panel.
export async function saveDelivery(id: string, delivery: Omit<DeliveryInfo, 'updatedAt'>): Promise<DeliveryInfo> {
  return requestJSON<DeliveryInfo>(`/api/v1/tasks/${id}/delivery`, 'PUT', delivery)
}

// deployTask starts or replaces both generated project processes. Zero-valued
// ports ask the backend to allocate currently available localhost ports.
export async function deployTask(id: string, input: DeployTaskInput): Promise<DeliveryInfo> {
  return requestJSON<DeliveryInfo>(`/api/v1/tasks/${id}/deploy`, 'POST', input)
}

// setNodeStatus applies an approval, completion, failure, or retry state.
export async function setNodeStatus(id: string, nodeKey: string, status: string): Promise<RuntimeState> {
  return requestJSON<RuntimeState>(`/api/v1/tasks/${id}/nodes/${nodeKey}/status`, 'POST', { status })
}

// runNode starts the node assignee's Claude Code session.
export async function runNode(id: string, nodeKey: string): Promise<RuntimeSession | { runtime: RuntimeState }> {
  return requestJSON<RuntimeSession | { runtime: RuntimeState }>(`/api/v1/tasks/${id}/nodes/${nodeKey}/run`, 'POST')
}

// sendTaskMessage routes text to a selected task member. Restart marks a fresh
// page entry, but the backend still reuses any live idle process.
export async function sendTaskMessage(id: string, memberId: string, text: string, restart = false): Promise<RuntimeSession> {
  return requestJSON<RuntimeSession>(`/api/v1/tasks/${id}/messages`, 'POST', { memberId, text, restart })
}

// createChange appends a new requirement or test defect.
export async function createChange(id: string, description: string, source = 'USER'): Promise<ChangeRequest> {
  return requestJSON<ChangeRequest>(`/api/v1/tasks/${id}/changes`, 'POST', { description, source })
}

// listTaskFiles returns the managed task file tree.
export async function listTaskFiles(id: string): Promise<TaskFile[]> {
  return (await requestJSON<{ items: TaskFile[] }>(`/api/v1/tasks/${id}/files`, 'GET')).items
}

// openTaskFile asks the local control plane to reveal one validated task file.
export async function openTaskFile(id: string, path: string): Promise<void> {
  await requestJSON(`/api/v1/tasks/${id}/files/open`, 'POST', { path })
}

// uploadTaskFile stores planning material below the task inputs directory.
export async function uploadTaskFile(id: string, file: File): Promise<{ path: string }> {
  const form = new FormData()
  form.append('file', file)
  const response = await fetch(`/api/v1/tasks/${id}/files`, { method: 'POST', body: form })
  return parseResponse<{ path: string }>(response)
}

// listAssets returns one versioned catalog root.
export async function listAssets(kind: AssetKind): Promise<Asset[]> {
  return (await requestJSON<{ items: Asset[] }>(`/api/v1/assets/${kind}`, 'GET')).items
}

// createAsset creates a user-owned catalog item.
export async function createAsset(kind: AssetKind, input: AssetInput): Promise<Asset> {
  return requestJSON<Asset>(`/api/v1/assets/${kind}`, 'POST', input)
}

// updateAsset changes metadata, enabled state, default skills, or selected version.
export async function updateAsset(kind: AssetKind, id: string, input: AssetInput): Promise<Asset> {
  return requestJSON<Asset>(`/api/v1/assets/${kind}/${id}`, 'PUT', input)
}

// addAssetVersion publishes a new immutable Markdown version.
export async function addAssetVersion(kind: AssetKind, id: string, version: string, body: string): Promise<Asset> {
  return requestJSON<Asset>(`/api/v1/assets/${kind}/${id}/versions`, 'POST', { version, body, select: true })
}

// deleteAsset removes a non-system unreferenced asset.
export async function deleteAsset(kind: AssetKind, id: string): Promise<void> {
  return requestJSON<void>(`/api/v1/assets/${kind}/${id}`, 'DELETE')
}

// getAssetRelations returns task and agent references.
export async function getAssetRelations(kind: AssetKind, id: string): Promise<{ taskIds: string[]; agentIds: string[] }> {
  return requestJSON<{ taskIds: string[]; agentIds: string[] }>(`/api/v1/assets/${kind}/${id}/relations`, 'GET')
}

// listSessionEvents polls ordered runtime events after sequence.
export async function listSessionEvents(sessionId: string, after: number): Promise<RuntimeEvent[]> {
  return (await requestJSON<{ items: RuntimeEvent[] }>(`/api/v1/sessions/${sessionId}/events?after=${after}`, 'GET')).items
}

// listRuntimeSessions returns all active and recently exited local Claude sessions.
export async function listRuntimeSessions(): Promise<RuntimeSession[]> {
  return (await requestJSON<{ items: RuntimeSession[] }>('/api/v1/sessions', 'GET')).items
}

// listTaskSessions includes live sessions and task-local archived session logs.
export async function listTaskSessions(taskId: string): Promise<RuntimeSession[]> {
  return (await requestJSON<{ items: RuntimeSession[] }>(`/api/v1/tasks/${taskId}/sessions`, 'GET')).items
}

// listTaskSessionEvents reads live events or reconstructs an archived log stream.
export async function listTaskSessionEvents(taskId: string, sessionId: string, after: number): Promise<RuntimeEvent[]> {
  return (await requestJSON<{ items: RuntimeEvent[] }>(`/api/v1/tasks/${taskId}/sessions/${sessionId}/events?after=${after}`, 'GET')).items
}

// getSession reconnects the UI to one runtime session retained by the backend.
export async function getSession(sessionId: string): Promise<RuntimeSession> {
  return requestJSON<RuntimeSession>(`/api/v1/sessions/${sessionId}`, 'GET')
}

// decidePermission approves or denies one Claude Code tool request.
export async function decidePermission(sessionId: string, requestId: string, allow: boolean, updatedInput: Record<string, unknown>): Promise<void> {
  await requestJSON(`/api/v1/sessions/${sessionId}/permissions`, 'POST', { requestId, allow, updatedInput, message: allow ? '' : 'User denied this operation' })
}

// interruptSession asks Claude Code to stop its current turn while keeping the session.
export async function interruptSession(sessionId: string): Promise<void> {
  await requestJSON(`/api/v1/sessions/${sessionId}/interrupt`, 'POST')
}

// stopSession terminates a Claude Code process.
export async function stopSession(sessionId: string): Promise<void> {
  await requestJSON(`/api/v1/sessions/${sessionId}/stop`, 'POST')
}
