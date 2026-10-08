// Package task owns task metadata and its file-backed lifecycle.
package task

import "time"

const (
	// StatusDraft means the task directory exists but its team and workflow are not confirmed.
	StatusDraft = "DRAFT"
	// PhasePlanning means the task is still collecting material and building a plan.
	PhasePlanning = "PLANNING"
	// StatusReady means the team and workflow are confirmed and can be started.
	StatusReady = "READY"
	// StatusPlanning covers team building, intent alignment, architecture, and prototype work.
	StatusPlanning = "PLANNING"
	// StatusRunning means at least one workflow activity may execute.
	StatusRunning = "RUNNING"
	// StatusPaused means the user has stopped automatic workflow progress.
	StatusPaused = "PAUSED"
	// StatusAcceptance means delivery is ready for explicit user confirmation.
	StatusAcceptance = "ACCEPTANCE"
	// StatusCompleted means the user accepted the final delivery.
	StatusCompleted = "COMPLETED"
	// StatusClosed means the user stopped all task activity permanently while
	// retaining the workspace and its history for read-only inspection.
	StatusClosed = "CLOSED"
)

// Task is the small task.json document at the root of every task workspace.
// Source code, documents, runtime state, and logs live beside this document.
type Task struct {
	// SchemaVersion controls compatible task.json decoding and future migrations.
	SchemaVersion int `json:"schemaVersion"`
	// Revision increments on every metadata update to detect conflicting writes.
	Revision int64 `json:"revision"`
	// ID is the immutable task directory name and API identifier.
	ID string `json:"id"`
	// Name is the user-facing task name and can change without renaming the directory.
	Name string `json:"name"`
	// Description records the initial project background and expected outcome.
	Description string `json:"description"`
	// GroupID is the stable logical collection used to aggregate related tasks in navigation.
	GroupID string `json:"groupId"`
	// GroupName is copied into task metadata so groups remain portable without a central index.
	GroupName string `json:"groupName"`
	// FrontendPort is allocated at creation and passed to the team instead of asking the user for an address.
	FrontendPort int `json:"frontendPort"`
	// BackendPort is the paired local service port reserved for the task backend.
	BackendPort int `json:"backendPort"`
	// Status is the high-level task lifecycle state.
	Status string `json:"status"`
	// Phase is the current product-development phase.
	Phase string `json:"phase"`
	// Workspace is always dot for managed tasks because the task directory is the workspace.
	Workspace string `json:"workspace"`
	// CreatedAt is stored in UTC for portable task archives.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is the UTC timestamp of the last task metadata change.
	UpdatedAt time.Time `json:"updatedAt"`
}

// TaskGroup is the durable navigation container for tasks. It is persisted
// independently so deleting the final task never removes the user's group.
type TaskGroup struct {
	// ID is the stable group route referenced by task.json files.
	ID string `json:"id"`
	// Name is the user-facing group label.
	Name string `json:"name"`
	// CreatedAt records when the group was first introduced.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt records the latest task or group-name activity.
	UpdatedAt time.Time `json:"updatedAt"`
}

// taskGroupIndex is the small file-backed registry stored beside task folders.
type taskGroupIndex struct {
	// SchemaVersion protects future registry migrations.
	SchemaVersion int `json:"schemaVersion"`
	// Groups retains empty groups as well as groups with active tasks.
	Groups []TaskGroup `json:"groups"`
}

// CreateInput contains the user-controlled fields accepted when creating a task.
type CreateInput struct {
	// Name is the required task display name.
	Name string `json:"name"`
	// Description is the optional project background shown to the planning agent.
	Description string `json:"description"`
	// GroupID reuses an existing task group; an empty value requests a generated identifier.
	GroupID string `json:"groupId"`
	// GroupName names a newly created group or refreshes the display name of an existing group.
	GroupName string `json:"groupName"`
}
