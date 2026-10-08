package task

import "time"

// TeamMember is one task-specific binding between a role and an agent version.
type TeamMember struct {
	// ID is stable inside the task and routes chat and workflow nodes.
	ID string `json:"id"`
	// Role is the semantic team responsibility.
	Role string `json:"role"`
	// Name is the user-facing member name.
	Name string `json:"name"`
	// AgentID references the global catalog asset.
	AgentID string `json:"agentId"`
	// AgentVersion freezes the selected catalog version for the task.
	AgentVersion string `json:"agentVersion"`
	// SkillIDs are materialized into the task workspace when it starts.
	SkillIDs []string `json:"skillIds"`
	// RuleIDs apply only to this member in addition to task-wide rules.
	RuleIDs []string `json:"ruleIds"`
	// KnowledgeIDs provide member-specific local or MCP knowledge sources.
	KnowledgeIDs []string `json:"knowledgeIds"`
	// Required prevents deletion of the project manager.
	Required bool `json:"required"`
}

// WorkflowNode is an editable DAG node with explicit dependency semantics.
type WorkflowNode struct {
	// Key is unique within one task workflow.
	Key string `json:"key"`
	// Name is the concise UI label.
	Name string `json:"name"`
	// Type is AGENT or USER_APPROVAL.
	Type string `json:"type"`
	// AssigneeID points to a team member and is empty for user approval nodes.
	AssigneeID string `json:"assigneeId,omitempty"`
	// Phase groups nodes into the product-development lifecycle.
	Phase string `json:"phase"`
	// Background allows a preparation node to run as soon as its dependencies
	// are ready, even while the user reviews an earlier visible phase. Such work
	// must stay reversible: contracts, types, stubs, and project skeletons only.
	Background bool `json:"background,omitempty"`
	// WaitFor lists predecessor node keys.
	WaitFor []string `json:"waitFor"`
	// DependencyMode is ALL or ANY.
	DependencyMode string `json:"dependencyMode"`
	// Status is PENDING, READY, RUNNING, WAITING_USER, SUCCEEDED, FAILED, or SKIPPED.
	Status string `json:"status"`
	// Description is the executable task and expected outcome.
	Description string `json:"description"`
}

// Plan is the confirmed team and workflow stored below .cc-hardness.
type Plan struct {
	// SchemaVersion controls compatible plan decoding.
	SchemaVersion int `json:"schemaVersion"`
	// Revision increments whenever the user edits team or workflow.
	Revision int64 `json:"revision"`
	// BuilderAgentID identifies the task-building agent that produced the initial plan.
	BuilderAgentID string `json:"builderAgentId"`
	// BuilderAgentVersion freezes the builder policy used for this task.
	BuilderAgentVersion string `json:"builderAgentVersion"`
	// TaskRuleIDs apply to every member.
	TaskRuleIDs []string `json:"taskRuleIds"`
	// TaskKnowledgeIDs apply to every member.
	TaskKnowledgeIDs []string `json:"taskKnowledgeIds"`
	// ConfigSetIDs attach reusable environment and middleware configurations.
	ConfigSetIDs []string `json:"configSetIds"`
	// Team contains selected agent versions and capabilities.
	Team []TeamMember `json:"team"`
	// Workflow is the editable DAG definition and current lightweight status view.
	Workflow []WorkflowNode `json:"workflow"`
	// UpdatedAt records the latest plan edit in UTC.
	UpdatedAt time.Time `json:"updatedAt"`
}

// PlanMutation requests one validated, revisioned team-plan change through the
// control-plane API instead of allowing an agent or UI to edit JSON directly.
type PlanMutation struct {
	// Action selects ADD_MEMBER, REMOVE_MEMBER, UPDATE_MEMBER,
	// REPLACE_WORKFLOW, or UPDATE_TASK_RESOURCES.
	Action string `json:"action"`
	// AgentID selects a catalog agent when ADD_MEMBER should use current defaults.
	AgentID string `json:"agentId,omitempty"`
	// MemberID identifies the existing member affected by remove or update.
	MemberID string `json:"memberId,omitempty"`
	// Member carries an exact add/restore binding or the complete updated member.
	Member *TeamMember `json:"member,omitempty"`
	// Workflow optionally replaces the DAG in the same atomic member mutation.
	Workflow *[]WorkflowNode `json:"workflow,omitempty"`
	// TaskRuleIDs replaces task-wide rules for UPDATE_TASK_RESOURCES.
	TaskRuleIDs *[]string `json:"taskRuleIds,omitempty"`
	// TaskKnowledgeIDs replaces task-wide knowledge for UPDATE_TASK_RESOURCES.
	TaskKnowledgeIDs *[]string `json:"taskKnowledgeIds,omitempty"`
	// ConfigSetIDs replaces task-bound configuration collections.
	ConfigSetIDs *[]string `json:"configSetIds,omitempty"`
}

// RuntimeState persists workflow execution state separately from the editable definition.
type RuntimeState struct {
	// SchemaVersion controls compatible runtime decoding.
	SchemaVersion int `json:"schemaVersion"`
	// Revision increments on every node state transition.
	Revision int64 `json:"revision"`
	// NodeStates maps node keys to their current execution state.
	NodeStates map[string]string `json:"nodeStates"`
	// ActiveSessions maps member IDs to live or resumable session IDs.
	ActiveSessions map[string]string `json:"activeSessions"`
	// UpdatedAt is the latest runtime transition time.
	UpdatedAt time.Time `json:"updatedAt"`
}

// ChangeRequest is a user request or test defect routed through the project manager.
type ChangeRequest struct {
	// ID uniquely identifies the append-only change record.
	ID string `json:"id"`
	// Source is USER or TEST.
	Source string `json:"source"`
	// Description is the requested behavior or defect summary.
	Description string `json:"description"`
	// Status is OPEN, PLANNED, RUNNING, or COMPLETED.
	Status string `json:"status"`
	// CreatedAt is the UTC record time.
	CreatedAt time.Time `json:"createdAt"`
}

// TaskDetail combines task metadata, plan, runtime, and change history for the workspace UI.
type TaskDetail struct {
	Task
	Plan     *Plan           `json:"plan,omitempty"`
	Runtime  *RuntimeState   `json:"runtime,omitempty"`
	Changes  []ChangeRequest `json:"changes"`
	Delivery *DeliveryInfo   `json:"delivery,omitempty"`
	RootPath string          `json:"rootPath"`
}

// DeliveryInfo stores the persistent deployment created from a completed task.
// PIDs are retained so redeployment and task deletion can stop the exact process
// trees even after the browser has been refreshed.
type DeliveryInfo struct {
	FrontendURL  string    `json:"frontendUrl"`
	BackendURL   string    `json:"backendUrl"`
	FrontendPort int       `json:"frontendPort"`
	BackendPort  int       `json:"backendPort"`
	FrontendPID  int       `json:"frontendPid"`
	BackendPID   int       `json:"backendPid"`
	Status       string    `json:"status"`
	Notes        string    `json:"notes"`
	StartedAt    time.Time `json:"startedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// DeployInput contains optional user-selected ports. A zero value asks the
// control plane to allocate an available localhost port.
type DeployInput struct {
	FrontendPort int `json:"frontendPort"`
	BackendPort  int `json:"backendPort"`
}

// NodeStatusInput requests a controlled workflow node transition.
type NodeStatusInput struct {
	Status string `json:"status"`
}

// ChangeInput contains a new user requirement or defect report.
type ChangeInput struct {
	Source      string `json:"source"`
	Description string `json:"description"`
}
