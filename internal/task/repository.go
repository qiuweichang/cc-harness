package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cc-hardness/internal/filedata"
)

var (
	// ErrNotFound is returned when no task directory exists for an identifier.
	ErrNotFound = errors.New("task not found")
	// ErrInvalidID is returned when an identifier could escape the managed task root.
	ErrInvalidID = errors.New("invalid task id")
)

// Repository persists every task as an independent directory below data/tasks.
// The process-level lock protects directory scans and task creation from racing;
// later task updates will add narrower per-task locks while retaining this boundary.
type Repository struct {
	// tasksRoot is the canonical absolute path containing all managed task folders.
	tasksRoot string
	// groupsPath stores group metadata independently from task directories so an
	// empty group survives deletion of its final task.
	groupsPath string
	// mu coordinates task directory creation with concurrent list operations.
	mu sync.RWMutex
}

// NewRepository creates the managed data directory structure and returns a repository.
// It does not create a database or a central authoritative index.
func NewRepository(dataRoot string) (*Repository, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, errors.New("data root is required")
	}
	tasksRoot := filepath.Join(dataRoot, "tasks")
	for _, directory := range []string{
		filepath.Join(dataRoot, "catalog", "agents"),
		filepath.Join(dataRoot, "catalog", "skills"),
		filepath.Join(dataRoot, "catalog", "rules"),
		filepath.Join(dataRoot, "catalog", "knowledge-bases"),
		filepath.Join(dataRoot, "config"),
		tasksRoot,
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create data directory %s: %w", directory, err)
		}
	}
	return &Repository{tasksRoot: tasksRoot, groupsPath: filepath.Join(dataRoot, "task-groups.json")}, nil
}

// List scans task.json from every first-level task directory.
// A malformed task is returned as an error so corruption is visible instead of
// silently disappearing from the Web task list.
func (r *Repository) List(ctx context.Context) ([]Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(r.tasksRoot)
	if err != nil {
		return nil, fmt.Errorf("read task root: %w", err)
	}
	tasks := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, readErr := r.readTask(filepath.Join(r.tasksRoot, entry.Name(), "task.json"))
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read task %s: %w", entry.Name(), readErr)
		}
		tasks = append(tasks, item)
	}
	sort.Slice(tasks, func(left int, right int) bool {
		return tasks[left].UpdatedAt.After(tasks[right].UpdatedAt)
	})
	return tasks, nil
}

// ListGroups returns persisted groups merged with legacy task metadata. Legacy
// groups become durable when their final task is deleted or a new task reuses them.
func (r *Repository) ListGroups(ctx context.Context) ([]TaskGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index, err := r.readGroupIndex()
	if err != nil {
		return nil, err
	}
	grouped := make(map[string]TaskGroup, len(index.Groups))
	for _, group := range index.Groups {
		grouped[group.ID] = group
	}
	entries, err := os.ReadDir(r.tasksRoot)
	if err != nil {
		return nil, fmt.Errorf("read task root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, readErr := r.readTask(filepath.Join(r.tasksRoot, entry.Name(), "task.json"))
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		group, exists := grouped[item.GroupID]
		if !exists {
			group = TaskGroup{ID: item.GroupID, Name: item.GroupName, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
		}
		if item.GroupName != "" {
			group.Name = item.GroupName
		}
		if item.UpdatedAt.After(group.UpdatedAt) {
			group.UpdatedAt = item.UpdatedAt
		}
		grouped[group.ID] = group
	}
	groups := make([]TaskGroup, 0, len(grouped))
	for _, group := range grouped {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(left int, right int) bool {
		return groups[left].UpdatedAt.After(groups[right].UpdatedAt)
	})
	return groups, nil
}

// Get reads one task.json after validating that id is a single safe path segment.
func (r *Repository) Get(ctx context.Context, id string) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	item, err := r.readTask(filepath.Join(r.tasksRoot, id, "task.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Task{}, ErrNotFound
	}
	return item, err
}

// Create allocates a task directory and writes its initial task.json document.
// The task directory itself is the working directory, so no nested workspace is created.
func (r *Repository) Create(ctx context.Context, input CreateInput) (Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}

	id, err := newTaskID()
	if err != nil {
		return Task{}, fmt.Errorf("generate task id: %w", err)
	}
	now := time.Now().UTC()
	frontendPort, backendPort := taskPorts(id)
	item := Task{
		SchemaVersion: 1,
		Revision:      1,
		ID:            id,
		Name:          strings.TrimSpace(input.Name),
		Description:   strings.TrimSpace(input.Description),
		GroupID:       strings.TrimSpace(input.GroupID),
		GroupName:     strings.TrimSpace(input.GroupName),
		FrontendPort:  frontendPort,
		BackendPort:   backendPort,
		Status:        StatusDraft,
		Phase:         PhasePlanning,
		Workspace:     ".",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if item.GroupID == "" {
		item.GroupID = "grp_" + hex.EncodeToString([]byte(item.ID[len(item.ID)-10:]))
	}
	if item.GroupName == "" {
		item.GroupName = "默认任务组"
	}
	if err := r.saveGroupLocked(TaskGroup{ID: item.GroupID, Name: item.GroupName, CreatedAt: now, UpdatedAt: now}); err != nil {
		return Task{}, fmt.Errorf("persist task group: %w", err)
	}

	taskRoot := filepath.Join(r.tasksRoot, id)
	if err := createTaskDirectories(taskRoot); err != nil {
		return Task{}, err
	}
	if err := writeJSONAtomically(filepath.Join(taskRoot, "task.json"), item); err != nil {
		return Task{}, fmt.Errorf("write task metadata: %w", err)
	}
	return item, nil
}

// Root returns the canonical task workspace path after validating the task ID.
func (r *Repository) Root(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	root := filepath.Join(r.tasksRoot, id)
	if _, err := os.Stat(filepath.Join(root, "task.json")); errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	return root, nil
}

// Delete removes one complete managed task workspace after validating that the
// resolved directory is an immediate child of tasksRoot. Callers must stop task
// processes before invoking this method so no process writes into a removed tree.
func (r *Repository) Delete(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.Clean(filepath.Join(r.tasksRoot, id))
	relative, err := filepath.Rel(r.tasksRoot, root)
	if err != nil || relative != id || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalidID
	}
	item, err := r.readTask(filepath.Join(root, "task.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := r.saveGroupLocked(TaskGroup{ID: item.GroupID, Name: item.GroupName, CreatedAt: item.CreatedAt, UpdatedAt: now}); err != nil {
		return fmt.Errorf("preserve task group: %w", err)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("delete task workspace: %w", err)
	}
	return nil
}

// readGroupIndex loads the independent group registry and treats a missing file
// as an empty first-run registry.
func (r *Repository) readGroupIndex() (taskGroupIndex, error) {
	var index taskGroupIndex
	if err := filedata.ReadJSON(r.groupsPath, &index); errors.Is(err, os.ErrNotExist) {
		return taskGroupIndex{SchemaVersion: 1, Groups: []TaskGroup{}}, nil
	} else if err != nil {
		return taskGroupIndex{}, err
	}
	if index.SchemaVersion != 1 {
		return taskGroupIndex{}, errors.New("task group index uses an unsupported schema")
	}
	return index, nil
}

// saveGroupLocked inserts or refreshes one group while the caller holds the
// repository write lock. Existing creation timestamps are never rewritten.
func (r *Repository) saveGroupLocked(group TaskGroup) error {
	index, err := r.readGroupIndex()
	if err != nil {
		return err
	}
	found := false
	for position := range index.Groups {
		if index.Groups[position].ID != group.ID {
			continue
		}
		if group.Name != "" {
			index.Groups[position].Name = group.Name
		}
		index.Groups[position].UpdatedAt = group.UpdatedAt
		found = true
		break
	}
	if !found {
		index.Groups = append(index.Groups, group)
	}
	return filedata.WriteJSON(r.groupsPath, index)
}

// SaveTask atomically replaces task.json while holding the repository write lock.
func (r *Repository) SaveTask(item Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateID(item.ID); err != nil {
		return err
	}
	return filedata.WriteJSON(filepath.Join(r.tasksRoot, item.ID, "task.json"), item)
}

// LoadPlan reads the confirmed task plan, returning nil when planning has not run.
func (r *Repository) LoadPlan(id string) (*Plan, error) {
	root, err := r.Root(id)
	if err != nil {
		return nil, err
	}
	var plan Plan
	err = filedata.ReadJSON(filepath.Join(root, ".cc-hardness", "team-plan.json"), &plan)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return &plan, err
}

// SavePlan persists team and DAG as one revisioned document.
func (r *Repository) SavePlan(id string, plan Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateID(id); err != nil {
		return err
	}
	return filedata.WriteJSON(filepath.Join(r.tasksRoot, id, ".cc-hardness", "team-plan.json"), plan)
}

// LoadRuntime reads workflow execution state when the task has started.
func (r *Repository) LoadRuntime(id string) (*RuntimeState, error) {
	root, err := r.Root(id)
	if err != nil {
		return nil, err
	}
	var state RuntimeState
	err = filedata.ReadJSON(filepath.Join(root, ".cc-hardness", "workflow", "runtime-state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return &state, err
}

// SaveRuntime atomically writes current node and session state.
func (r *Repository) SaveRuntime(id string, state RuntimeState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateID(id); err != nil {
		return err
	}
	return filedata.WriteJSON(filepath.Join(r.tasksRoot, id, ".cc-hardness", "workflow", "runtime-state.json"), state)
}

// AppendChange stores one append-only change request in the task audit area.
func (r *Repository) AppendChange(id string, change ChangeRequest) error {
	root, err := r.Root(id)
	if err != nil {
		return err
	}
	return filedata.AppendJSONLine(filepath.Join(root, ".cc-hardness", "messages", "change-requests.jsonl"), change)
}

// LoadChanges returns all complete change request records.
func (r *Repository) LoadChanges(id string) ([]ChangeRequest, error) {
	root, err := r.Root(id)
	if err != nil {
		return nil, err
	}
	return filedata.ReadJSONLines[ChangeRequest](filepath.Join(root, ".cc-hardness", "messages", "change-requests.jsonl"))
}

// LoadDelivery reads the final service addresses when the delivery node has published them.
func (r *Repository) LoadDelivery(id string) (*DeliveryInfo, error) {
	root, err := r.Root(id)
	if err != nil {
		return nil, err
	}
	var delivery DeliveryInfo
	err = filedata.ReadJSON(filepath.Join(root, ".cc-hardness", "artifacts", "delivery.json"), &delivery)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return &delivery, err
}

// SaveDelivery atomically writes user-visible delivery addresses and notes.
func (r *Repository) SaveDelivery(id string, delivery DeliveryInfo) error {
	root, err := r.Root(id)
	if err != nil {
		return err
	}
	return filedata.WriteJSON(filepath.Join(root, ".cc-hardness", "artifacts", "delivery.json"), delivery)
}

// readTask decodes one task.json file and validates its minimal schema fields.
func (r *Repository) readTask(path string) (Task, error) {
	file, err := os.Open(path)
	if err != nil {
		return Task{}, err
	}
	defer file.Close()
	var item Task
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&item); err != nil {
		return Task{}, fmt.Errorf("decode json: %w", err)
	}
	if item.SchemaVersion != 1 || item.ID == "" || item.Name == "" {
		return Task{}, errors.New("task.json is missing required fields or uses an unsupported schema")
	}
	// Legacy task archives predate task groups; expose them through one stable virtual group
	// without rewriting user-owned metadata during a read-only list operation.
	if item.GroupID == "" {
		item.GroupID = "grp_legacy"
		item.GroupName = "历史任务"
	}
	if item.FrontendPort == 0 || item.BackendPort == 0 {
		item.FrontendPort, item.BackendPort = taskPorts(item.ID)
	}
	// Older task files used READY or RUNNING before development. Present those
	// phases through the current PLANNING lifecycle semantics.
	if (item.Status == StatusRunning || item.Status == StatusReady) && (item.Phase == PhasePlanning || item.Phase == "ALIGNMENT" || item.Phase == "ARCHITECTURE" || item.Phase == "PROTOTYPE") {
		item.Status = StatusPlanning
	}
	return item, nil
}

// createTaskDirectories prepares the platform-owned and project-facing folders.
func createTaskDirectories(taskRoot string) error {
	directories := []string{
		taskRoot,
		filepath.Join(taskRoot, ".claude", "agents"),
		filepath.Join(taskRoot, ".claude", "skills"),
		filepath.Join(taskRoot, ".claude", "rules"),
		filepath.Join(taskRoot, ".cc-hardness", "workflow", "versions"),
		filepath.Join(taskRoot, ".cc-hardness", "runtime"),
		filepath.Join(taskRoot, ".cc-hardness", "conversations"),
		filepath.Join(taskRoot, ".cc-hardness", "events"),
		filepath.Join(taskRoot, ".cc-hardness", "audit"),
		filepath.Join(taskRoot, ".cc-hardness", "runs"),
		filepath.Join(taskRoot, ".cc-hardness", "messages"),
		filepath.Join(taskRoot, ".cc-hardness", "artifacts"),
		filepath.Join(taskRoot, ".cc-hardness", "logs"),
		filepath.Join(taskRoot, ".cc-hardness", "recovery"),
		filepath.Join(taskRoot, "inputs"),
		// docs/agents is the fixed root for generated technical documents. Each
		// runtime member owns a child folder and phase subfolders beneath it.
		filepath.Join(taskRoot, "docs", "agents"),
		filepath.Join(taskRoot, "docs", "requirements"),
		filepath.Join(taskRoot, "docs", "architecture"),
		filepath.Join(taskRoot, "docs", "design"),
		filepath.Join(taskRoot, "docs", "test"),
		filepath.Join(taskRoot, "docs", "delivery"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create task directory %s: %w", directory, err)
		}
	}
	return nil
}

// writeJSONAtomically writes a complete JSON document through a same-directory
// temporary file so readers never observe a partially encoded state document.
func writeJSONAtomically(path string, value any) error {
	return filedata.WriteJSON(path, value)
}

// validateID rejects separators, traversal markers, and non-task identifiers.
func validateID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || !strings.HasPrefix(id, "tsk_") {
		return ErrInvalidID
	}
	return nil
}

// newTaskID returns a sortable task directory identifier with random collision protection.
func newTaskID() (string, error) {
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "tsk_" + time.Now().UTC().Format("20060102_150405") + "_" + hex.EncodeToString(random), nil
}

// taskPorts derives a stable high-port pair from the random task ID. The pair is
// portable with task.json and avoids an extra user-facing delivery-address step.
func taskPorts(taskID string) (int, int) {
	suffix := taskID
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	seed, _ := strconv.ParseUint(suffix, 16, 16)
	frontend := 20000 + int(seed%19000)
	return frontend, frontend + 1
}
