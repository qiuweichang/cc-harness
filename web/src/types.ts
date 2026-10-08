// Task describes the task.json summary returned by the Go API.
export interface Task {
  schemaVersion: number
  revision: number
  id: string
  name: string
  description: string
  groupId: string
  groupName: string
  frontendPort: number
  backendPort: number
  status: string
  phase: string
  workspace: string
  createdAt: string
  updatedAt: string
}

// CreateTaskInput contains the user-editable fields on the new-task dialog.
export interface CreateTaskInput {
  name: string
  description: string
  groupId: string
  groupName: string
}

// NewTaskRequest keeps browser File objects beside the JSON task metadata during creation.
export interface NewTaskRequest extends CreateTaskInput {
  files: File[]
}

// ClaudeInfo is the safe local runtime summary shown from the sidebar workspace entry.
export interface ClaudeInfo {
  detected: boolean
  version: string
  model: string
  modelSource: string
  permissionMode: string
  inputFormat: string
  outputFormat: string
}

// TaskGroupSummary is persisted independently so a group remains available after
// its final task is deleted.
export interface TaskGroupSummary {
  id: string
  name: string
  createdAt: string
  updatedAt: string
}

// TaskGroup combines durable group metadata with the current task-card records.
export interface TaskGroup {
  id: string
  name: string
  tasks: Task[]
  updatedAt: string
}

// AssetKind matches one file-backed catalog root.
export type AssetKind = 'agents' | 'skills' | 'rules' | 'knowledge-bases'

// Asset is a selected Markdown version plus mutable catalog metadata.
export interface Asset {
  schemaVersion: number
  revision: number
  id: string
  kind: AssetKind
  name: string
  description: string
  avatar?: string
  source: 'SYSTEM' | 'USER' | 'AI'
  role?: string
  enabled: boolean
  currentVersion: string
  versions: string[]
  defaultSkillIds?: string[]
  config?: Record<string, unknown>
  body: string
  createdAt: string
  updatedAt: string
}

// AssetInput creates or updates one catalog asset.
export interface AssetInput {
  id: string
  name: string
  description: string
  avatar: string
  source: string
  role: string
  enabled: boolean
  version: string
  defaultSkillIds: string[]
  config: Record<string, unknown>
  body: string
}

// TeamMember is one task-specific role and catalog version binding.
export interface TeamMember {
  id: string
  role: string
  name: string
  agentId: string
  agentVersion: string
  skillIds: string[]
  ruleIds: string[]
  knowledgeIds: string[]
  required: boolean
}

// WorkflowNode is one editable DAG node.
export interface WorkflowNode {
  key: string
  name: string
  type: 'AGENT' | 'USER_APPROVAL'
  assigneeId?: string
  phase: string
  // background marks reversible preparation that may execute before its phase
  // becomes the user's visible workspace.
  background?: boolean
  waitFor: string[]
  dependencyMode: 'ALL' | 'ANY'
  status: string
  description: string
}

// TaskPlan combines the selected team and workflow.
export interface TaskPlan {
  schemaVersion: number
  revision: number
  builderAgentId: string
  builderAgentVersion: string
  taskRuleIds: string[]
  taskKnowledgeIds: string[]
  configSetIds: string[]
  team: TeamMember[]
  workflow: WorkflowNode[]
  updatedAt: string
}

// TaskPlanMutation is one backend-validated atomic change shared by manual
// controls and team-building agents.
export interface TaskPlanMutation {
  action: 'ADD_MEMBER' | 'REMOVE_MEMBER' | 'UPDATE_MEMBER' | 'REPLACE_WORKFLOW' | 'UPDATE_TASK_RESOURCES'
  agentId?: string
  memberId?: string
  member?: TeamMember
  workflow?: WorkflowNode[]
  taskRuleIds?: string[]
  taskKnowledgeIds?: string[]
  configSetIds?: string[]
}

// ConfigurationEntry is one named raw middleware or environment configuration.
export interface ConfigurationEntry {
  id: string
  name: string
  content: string
}

// ConfigurationSet is one reusable collection attachable to task plans.
export interface ConfigurationSet {
  schemaVersion: number
  revision: number
  id: string
  name: string
  description: string
  entries: ConfigurationEntry[]
  createdAt: string
  updatedAt: string
}

// ConfigurationSetInput contains the editable configuration-set payload.
export interface ConfigurationSetInput {
  name: string
  description: string
  entries: ConfigurationEntry[]
}

// RuntimeState tracks current workflow node and session states.
export interface RuntimeState {
  schemaVersion: number
  revision: number
  nodeStates: Record<string, string>
  activeSessions: Record<string, string>
  updatedAt: string
}

// ChangeRequest is a user requirement or test defect awaiting project-manager routing.
export interface ChangeRequest {
  id: string
  source: string
  description: string
  status: string
  createdAt: string
}

// TaskDetail is the full workspace view model.
export interface TaskDetail extends Task {
  plan?: TaskPlan
  runtime?: RuntimeState
  changes: ChangeRequest[]
  delivery?: DeliveryInfo
  rootPath: string
}

// DeliveryInfo is the persisted process ownership and service-address record for
// a completed task deployment.
export interface DeliveryInfo {
  frontendUrl: string
  backendUrl: string
  frontendPort: number
  backendPort: number
  frontendPid: number
  backendPid: number
  status: string
  notes: string
  startedAt: string
  updatedAt: string
}

// DeployTaskInput uses zero ports to request available localhost allocations.
export interface DeployTaskInput {
  frontendPort: number
  backendPort: number
}

// RuntimeSession is a Claude Code child process route.
export interface RuntimeSession {
  id: string
  taskId: string
  memberId: string
  pid: number
  status: string
  // busy is true while the current Claude turn is still producing output.
  busy: boolean
  // reconnected marks a page-entry response that attached to existing work.
  reconnected?: boolean
  startedAt: string
}

// RuntimeEvent is one ordered Claude Code or process event.
export interface RuntimeEvent {
  sequence: number
  type: string
  payload?: unknown
  line?: string
  createdAt: string
}

// TaskFile is one path in the managed task workspace.
export interface TaskFile {
  path: string
  directory: boolean
  size: number
  updatedAt: string
}

// SectionKey identifies one stable primary navigation destination.
export type SectionKey = 'tasks' | 'agents' | 'skills' | 'rules' | 'knowledge' | 'configurations'
