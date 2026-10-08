package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cc-hardness/internal/catalog"
	"cc-hardness/internal/configset"
	"cc-hardness/internal/filedata"
)

// Service applies task creation rules before delegating persistence to Repository.
type Service struct {
	// repository is the only component allowed to map task domain objects to directories.
	repository *Repository
	// catalog resolves selected versions and materializes task runtime assets.
	catalog *catalog.Service
	// configSets resolves reusable environment and middleware configuration snapshots.
	configSets *configset.Service
	// planMu serializes read-modify-write mutations so concurrent UI and agent
	// requests cannot silently overwrite each other's team-plan revisions.
	planMu sync.Mutex
	// runtimeMu serializes workflow-state and active-session read-modify-write
	// operations. Parallel phase agents can finish at nearly the same time, so
	// this lock prevents one completion from overwriting another node's result.
	runtimeMu sync.Mutex
	// deploymentMu serializes deploy, redeploy, and delete-time shutdown so two
	// clicks cannot orphan child process trees or overwrite persisted PIDs.
	deploymentMu sync.Mutex
}

// NewService creates the task application service.
func NewService(repository *Repository, catalogService *catalog.Service, configSetService *configset.Service) *Service {
	return &Service{repository: repository, catalog: catalogService, configSets: configSetService}
}

// Detail returns task metadata together with optional plan, runtime, and changes.
func (s *Service) Detail(ctx context.Context, id string) (TaskDetail, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	plan, err := s.repository.LoadPlan(id)
	if err != nil {
		return TaskDetail{}, err
	}
	if plan != nil {
		normalizePlanCollections(plan)
	}
	runtimeState, err := s.repository.LoadRuntime(id)
	if err != nil {
		return TaskDetail{}, err
	}
	if runtimeState != nil {
		if runtimeState.NodeStates == nil {
			runtimeState.NodeStates = map[string]string{}
		}
		if runtimeState.ActiveSessions == nil {
			runtimeState.ActiveSessions = map[string]string{}
		}
	}
	changes, err := s.repository.LoadChanges(id)
	if err != nil {
		return TaskDetail{}, err
	}
	root, err := s.repository.Root(id)
	if err != nil {
		return TaskDetail{}, err
	}
	delivery, err := s.repository.LoadDelivery(id)
	if err != nil {
		return TaskDetail{}, err
	}
	return TaskDetail{Task: item, Plan: plan, Runtime: runtimeState, Changes: changes, Delivery: delivery, RootPath: root}, nil
}

// List returns tasks ordered by their last update time.
func (s *Service) List(ctx context.Context) ([]Task, error) {
	return s.repository.List(ctx)
}

// ListGroups returns durable task-group navigation metadata, including groups
// whose final task has been deleted.
func (s *Service) ListGroups(ctx context.Context) ([]TaskGroup, error) {
	return s.repository.ListGroups(ctx)
}

// Get returns a single task by immutable task ID.
func (s *Service) Get(ctx context.Context, id string) (Task, error) {
	return s.repository.Get(ctx, id)
}

// Delete permanently removes the complete file-backed task workspace. Runtime
// processes are owned by the HTTP/runtime layer and must be stopped beforehand.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repository.Delete(ctx, id)
}

// Close transitions a task into its terminal read-only state while retaining all
// task files, plans, logs, and archived conversations for later inspection.
func (s *Service) Close(ctx context.Context, id string) (Task, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if item.Status == StatusClosed {
		return item, nil
	}
	item.Status = StatusClosed
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveTask(item); err != nil {
		return Task{}, err
	}
	return item, nil
}

// Create validates user input and creates a self-contained task workspace.
func (s *Service) Create(ctx context.Context, input CreateInput) (Task, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return Task{}, errors.New("task name is required")
	}
	if len([]rune(input.Name)) > 120 {
		return Task{}, errors.New("task name must not exceed 120 characters")
	}
	if len([]rune(input.Description)) > 8000 {
		return Task{}, errors.New("task description must not exceed 8000 characters")
	}
	input.GroupID = strings.TrimSpace(input.GroupID)
	input.GroupName = strings.TrimSpace(input.GroupName)
	if input.GroupID != "" && (filepath.Base(input.GroupID) != input.GroupID || !strings.HasPrefix(input.GroupID, "grp_")) {
		return Task{}, errors.New("task group id is invalid")
	}
	if len([]rune(input.GroupName)) > 80 {
		return Task{}, errors.New("task group name must not exceed 80 characters")
	}
	return s.repository.Create(ctx, input)
}

// GeneratePlan creates the required team and a safe default full-stack DAG.
// It uses catalog roles so changing the selected agent version changes future plans.
func (s *Service) GeneratePlan(ctx context.Context, id string) (Plan, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if item.Status == StatusClosed {
		return Plan{}, errors.New("closed task is read-only")
	}
	builder, err := s.catalog.FindAgentByRole("TASK_BUILDER")
	if err != nil {
		return Plan{}, fmt.Errorf("required task builder is unavailable: %w", err)
	}
	root, err := s.repository.Root(id)
	if err != nil {
		return Plan{}, err
	}
	inputFiles, err := planningInputFiles(root)
	if err != nil {
		return Plan{}, err
	}
	roles := []struct {
		Role     string
		MemberID string
		Required bool
	}{
		{"PROJECT_MANAGER", "project-manager", true},
		{"PRODUCT_MANAGER", "product-manager", false},
		{"ARCHITECT", "architect", false},
		{"UI_DESIGNER", "ui-designer", false},
		{"FRONTEND_ENGINEER", "frontend-engineer", false},
		{"BACKEND_ENGINEER", "backend-engineer", false},
		{"TEST_ENGINEER", "test-engineer", false},
		{"AUDITOR", "auditor", false},
	}
	team := make([]TeamMember, 0, len(roles))
	for _, role := range roles {
		agent, err := s.catalog.FindAgentByRole(role.Role)
		if err != nil {
			return Plan{}, fmt.Errorf("required agent role %s is unavailable: %w", role.Role, err)
		}
		team = append(team, TeamMember{
			ID: role.MemberID, Role: role.Role, Name: agent.Name,
			AgentID: agent.ID, AgentVersion: agent.CurrentVersion,
			SkillIDs: append([]string{}, agent.DefaultSkillIDs...), Required: role.Required,
		})
	}
	now := time.Now().UTC()
	plan := Plan{
		SchemaVersion: 1, Revision: 1,
		BuilderAgentID: builder.ID, BuilderAgentVersion: builder.CurrentVersion,
		TaskRuleIDs:  []string{"coding-quality", "security-baseline", "delivery-quality"},
		ConfigSetIDs: []string{},
		Team:         team, Workflow: defaultWorkflow(), UpdatedAt: now,
	}
	normalizePlanCollections(&plan)
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	if err := s.validatePlanAssets(plan); err != nil {
		return Plan{}, err
	}
	if err := s.repository.SavePlan(id, plan); err != nil {
		return Plan{}, err
	}
	if err := filedata.WriteText(filepath.Join(root, ".cc-hardness", "planning", "task-builder.md"), builder.Body); err != nil {
		return Plan{}, err
	}
	if err := s.materializeAgentSkills(root, builder); err != nil {
		return Plan{}, err
	}
	planningInput := fmt.Sprintf("# Task Planning Input\n\n## Name\n\n%s\n\n## Description\n\n%s\n\n## Allocated Local Ports\n\n- Frontend: %d\n- Backend: %d\n\n## Uploaded Files\n\n", item.Name, item.Description, item.FrontendPort, item.BackendPort)
	if len(inputFiles) == 0 {
		planningInput += "- None\n"
	} else {
		for _, inputFile := range inputFiles {
			planningInput += "- " + inputFile + "\n"
		}
	}
	if err := filedata.WriteText(filepath.Join(root, ".cc-hardness", "planning", "input.md"), planningInput); err != nil {
		return Plan{}, err
	}
	item.Status = StatusPlanning
	item.Phase = PhasePlanning
	item.Revision++
	item.UpdatedAt = now
	if err := s.repository.SaveTask(item); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// SavePlan validates user team and DAG edits and stores a new plan revision.
func (s *Service) SavePlan(ctx context.Context, id string, plan Plan) (Plan, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	return s.savePlan(ctx, id, plan)
}

// savePlan performs validation and persistence while its caller owns planMu.
func (s *Service) savePlan(ctx context.Context, id string, plan Plan) (Plan, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if item.Status == StatusClosed {
		return Plan{}, errors.New("closed task is read-only")
	}
	normalizePlanCollections(&plan)
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	current, err := s.repository.LoadPlan(id)
	if err != nil {
		return Plan{}, err
	}
	plan.SchemaVersion = 1
	plan.Revision = 1
	if current != nil {
		plan.Revision = current.Revision + 1
		if plan.BuilderAgentID == "" {
			plan.BuilderAgentID = current.BuilderAgentID
			plan.BuilderAgentVersion = current.BuilderAgentVersion
		}
	}
	if plan.BuilderAgentID == "" || plan.BuilderAgentVersion == "" {
		builder, err := s.catalog.FindAgentByRole("TASK_BUILDER")
		if err != nil {
			return Plan{}, fmt.Errorf("required task builder is unavailable: %w", err)
		}
		plan.BuilderAgentID = builder.ID
		plan.BuilderAgentVersion = builder.CurrentVersion
	}
	if err := s.validatePlanAssets(plan); err != nil {
		return Plan{}, err
	}
	plan.UpdatedAt = time.Now().UTC()
	if err := s.repository.SavePlan(id, plan); err != nil {
		return Plan{}, err
	}
	if runtimeState, err := s.repository.LoadRuntime(id); err != nil {
		return Plan{}, err
	} else if runtimeState != nil {
		nextStates := make(map[string]string, len(plan.Workflow))
		for _, node := range plan.Workflow {
			if currentStatus, exists := runtimeState.NodeStates[node.Key]; exists {
				nextStates[node.Key] = currentStatus
			} else if len(node.WaitFor) == 0 {
				nextStates[node.Key] = "READY"
			} else {
				nextStates[node.Key] = "PENDING"
			}
		}
		for _, node := range plan.Workflow {
			if nextStates[node.Key] == "PENDING" && dependenciesSatisfied(node, nextStates) {
				nextStates[node.Key] = "READY"
			}
		}
		runtimeState.NodeStates = nextStates
		runtimeState.Revision++
		runtimeState.UpdatedAt = time.Now().UTC()
		if err := s.repository.SaveRuntime(id, *runtimeState); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}

// ApplyPlanMutation changes one team-plan concern through a serialized backend
// operation. Optional workflow replacement lets an agent commit its semantic DAG
// rebuild atomically with the member change it was asked to perform.
func (s *Service) ApplyPlanMutation(ctx context.Context, id string, input PlanMutation) (Plan, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()

	current, err := s.repository.LoadPlan(id)
	if err != nil {
		return Plan{}, err
	}
	if current == nil {
		return Plan{}, errors.New("task plan is unavailable")
	}
	normalizePlanCollections(current)
	input.Action = strings.ToUpper(strings.TrimSpace(input.Action))

	switch input.Action {
	case "ADD_MEMBER":
		if err := s.addPlanMember(current, input); err != nil {
			return Plan{}, err
		}
	case "REMOVE_MEMBER":
		if err := removePlanMember(current, strings.TrimSpace(input.MemberID)); err != nil {
			return Plan{}, err
		}
	case "UPDATE_MEMBER":
		if err := updatePlanMember(current, strings.TrimSpace(input.MemberID), input.Member); err != nil {
			return Plan{}, err
		}
	case "REPLACE_WORKFLOW":
		if input.Workflow == nil {
			return Plan{}, errors.New("workflow is required")
		}
	case "UPDATE_TASK_RESOURCES":
		if input.TaskRuleIDs == nil && input.TaskKnowledgeIDs == nil && input.ConfigSetIDs == nil {
			return Plan{}, errors.New("task rules, knowledge, or configuration sets are required")
		}
		if input.TaskRuleIDs != nil {
			current.TaskRuleIDs = append([]string{}, (*input.TaskRuleIDs)...)
		}
		if input.TaskKnowledgeIDs != nil {
			current.TaskKnowledgeIDs = append([]string{}, (*input.TaskKnowledgeIDs)...)
		}
		if input.ConfigSetIDs != nil {
			current.ConfigSetIDs = append([]string{}, (*input.ConfigSetIDs)...)
		}
	default:
		return Plan{}, errors.New("unsupported team plan mutation")
	}
	if input.Workflow != nil {
		current.Workflow = append([]WorkflowNode{}, (*input.Workflow)...)
	}
	return s.savePlan(ctx, id, *current)
}

// addPlanMember resolves catalog defaults or restores an exact prior binding,
// then inserts a safe backend-generated workflow branch when no DAG is supplied.
func (s *Service) addPlanMember(plan *Plan, input PlanMutation) error {
	var member TeamMember
	if input.Member != nil {
		member = *input.Member
		if _, err := s.catalog.GetVersion(catalog.KindAgent, member.AgentID, member.AgentVersion); err != nil {
			return fmt.Errorf("agent version is unavailable: %w", err)
		}
	} else {
		agentID := strings.TrimSpace(input.AgentID)
		agent, err := s.catalog.Get(catalog.KindAgent, agentID)
		if err != nil {
			return fmt.Errorf("agent is unavailable: %w", err)
		}
		if !agent.Enabled {
			return errors.New("disabled agent cannot be added to a task")
		}
		member = TeamMember{
			ID: safeAvailableMemberID(plan.Team, agent.Role, agent.ID), Role: agent.Role,
			Name: agent.Name, AgentID: agent.ID, AgentVersion: agent.CurrentVersion,
			SkillIDs: append([]string{}, agent.DefaultSkillIDs...),
			RuleIDs:  []string{}, KnowledgeIDs: []string{},
		}
	}
	if !validMemberID(member.ID) {
		return errors.New("member id must contain lowercase letters, numbers, or hyphens")
	}
	if member.Role == "PROJECT_MANAGER" || member.ID == "project-manager" {
		return errors.New("the task already owns its required project manager")
	}
	for _, existing := range plan.Team {
		if existing.ID == member.ID {
			return errors.New("team member id already exists")
		}
	}
	member.Required = false
	normalizeMemberCollections(&member)
	plan.Team = append(plan.Team, member)
	if input.Workflow == nil {
		plan.Workflow = insertMemberWorkflow(plan.Workflow, member)
	}
	return nil
}

// removePlanMember deletes an optional member and bypasses every removed task
// dependency through its nearest surviving predecessors.
func removePlanMember(plan *Plan, memberID string) error {
	if memberID == "" {
		return errors.New("member id is required")
	}
	found := false
	for _, member := range plan.Team {
		if member.ID != memberID {
			continue
		}
		if member.Required || member.Role == "PROJECT_MANAGER" {
			return errors.New("required project manager cannot be removed")
		}
		found = true
		break
	}
	if !found {
		return errors.New("team member not found")
	}
	plan.Team = filterMembers(plan.Team, memberID)
	plan.Workflow = repairWorkflowAfterMemberRemoval(plan.Workflow, memberID)
	return nil
}

// updatePlanMember applies capability and version changes without permitting
// callers to change the member's stable identity, role, agent, or required flag.
func updatePlanMember(plan *Plan, memberID string, replacement *TeamMember) error {
	if memberID == "" || replacement == nil {
		return errors.New("member id and member payload are required")
	}
	for index := range plan.Team {
		existing := plan.Team[index]
		if existing.ID != memberID {
			continue
		}
		existing.AgentVersion = replacement.AgentVersion
		existing.SkillIDs = append([]string{}, replacement.SkillIDs...)
		existing.RuleIDs = append([]string{}, replacement.RuleIDs...)
		existing.KnowledgeIDs = append([]string{}, replacement.KnowledgeIDs...)
		normalizeMemberCollections(&existing)
		plan.Team[index] = existing
		return nil
	}
	return errors.New("team member not found")
}

// Start materializes selected catalog assets and initializes workflow runtime state.
func (s *Service) Start(ctx context.Context, id string) (TaskDetail, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	if item.Status == StatusClosed {
		return TaskDetail{}, errors.New("closed task is read-only")
	}
	plan, err := s.repository.LoadPlan(id)
	if err != nil {
		return TaskDetail{}, err
	}
	if plan == nil {
		return TaskDetail{}, errors.New("generate and confirm a team plan before starting")
	}
	normalizePlanCollections(plan)
	if err := validatePlan(*plan); err != nil {
		return TaskDetail{}, err
	}
	if err := s.validatePlanAssets(*plan); err != nil {
		return TaskDetail{}, err
	}
	root, err := s.repository.Root(id)
	if err != nil {
		return TaskDetail{}, err
	}
	if err := s.materializePlan(root, *plan); err != nil {
		return TaskDetail{}, err
	}
	nodeStates := make(map[string]string, len(plan.Workflow))
	for _, node := range plan.Workflow {
		state := "PENDING"
		if len(node.WaitFor) == 0 {
			state = "READY"
		}
		nodeStates[node.Key] = state
	}
	runtimeState := RuntimeState{
		SchemaVersion: 1, Revision: 1, NodeStates: nodeStates,
		ActiveSessions: map[string]string{}, UpdatedAt: time.Now().UTC(),
	}
	if err := s.repository.SaveRuntime(id, runtimeState); err != nil {
		return TaskDetail{}, err
	}
	item.Status = StatusPlanning
	item.Phase = "ALIGNMENT"
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveTask(item); err != nil {
		return TaskDetail{}, err
	}
	return s.Detail(ctx, id)
}

// SetNodeStatus applies a user or runtime transition and unlocks eligible successors.
func (s *Service) SetNodeStatus(ctx context.Context, id string, nodeKey string, status string) (RuntimeState, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return RuntimeState{}, err
	}
	if item.Status == StatusClosed {
		return RuntimeState{}, errors.New("closed task is read-only")
	}
	plan, err := s.repository.LoadPlan(id)
	if err != nil || plan == nil {
		return RuntimeState{}, errors.New("task plan is unavailable")
	}
	state, err := s.repository.LoadRuntime(id)
	if err != nil || state == nil {
		return RuntimeState{}, errors.New("task has not started")
	}
	allowed := map[string]bool{"PENDING": true, "READY": true, "RUNNING": true, "WAITING_USER": true, "SUCCEEDED": true, "FAILED": true, "SKIPPED": true}
	status = strings.ToUpper(strings.TrimSpace(status))
	if !allowed[status] {
		return RuntimeState{}, errors.New("invalid node status")
	}
	currentStatus, exists := state.NodeStates[nodeKey]
	if !exists {
		return RuntimeState{}, errors.New("workflow node not found")
	}
	if !nodeTransitionAllowed(currentStatus, status) {
		return RuntimeState{}, fmt.Errorf("node transition %s -> %s is not allowed", currentStatus, status)
	}
	state.NodeStates[nodeKey] = status
	for _, node := range plan.Workflow {
		if state.NodeStates[node.Key] != "PENDING" || len(node.WaitFor) == 0 {
			continue
		}
		if dependenciesSatisfied(node, state.NodeStates) {
			state.NodeStates[node.Key] = "READY"
		}
	}
	state.Revision++
	state.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveRuntime(id, *state); err != nil {
		return RuntimeState{}, err
	}
	item, taskErr := s.repository.Get(ctx, id)
	if taskErr == nil {
		// Phase changes are explicit user approvals handled by AdvanceStage.
		// A parallel DEVELOPMENT node may finish while the task is still on the
		// PROTOTYPE screen and must never move that screen forward implicitly.
		acceptanceStatus := state.NodeStates["acceptance"]
		if acceptanceStatus == "READY" || acceptanceStatus == "WAITING_USER" {
			item.Status = StatusAcceptance
		}
		item.Revision++
		item.UpdatedAt = time.Now().UTC()
		_ = s.repository.SaveTask(item)
	}
	return *state, nil
}

// SetActiveSession persists the latest Claude session route for one task member.
// Session process handles remain in memory, while this mapping lets the workspace
// explain which persisted task state belonged to the latest runtime conversation.
func (s *Service) SetActiveSession(ctx context.Context, id string, memberID string, sessionID string) (RuntimeState, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return RuntimeState{}, err
	}
	if item.Status == StatusClosed {
		return RuntimeState{}, errors.New("closed task is read-only")
	}
	if _, err := s.Member(id, memberID); err != nil {
		return RuntimeState{}, err
	}
	state, err := s.repository.LoadRuntime(id)
	if err != nil || state == nil {
		return RuntimeState{}, errors.New("task has not started")
	}
	if state.ActiveSessions == nil {
		state.ActiveSessions = map[string]string{}
	}
	state.ActiveSessions[memberID] = sessionID
	state.Revision++
	state.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveRuntime(id, *state); err != nil {
		return RuntimeState{}, err
	}
	return *state, nil
}

// AddChange appends a requirement or defect for project-manager triage.
func (s *Service) AddChange(ctx context.Context, id string, input ChangeInput) (ChangeRequest, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return ChangeRequest{}, err
	}
	if item.Status == StatusClosed {
		return ChangeRequest{}, errors.New("closed task is read-only")
	}
	input.Description = strings.TrimSpace(input.Description)
	if input.Description == "" {
		return ChangeRequest{}, errors.New("change description is required")
	}
	if input.Source == "" {
		input.Source = "USER"
	}
	change := ChangeRequest{ID: "chg_" + time.Now().UTC().Format("20060102_150405.000000000"), Source: strings.ToUpper(input.Source), Description: input.Description, Status: "OPEN", CreatedAt: time.Now().UTC()}
	if err := s.repository.AppendChange(id, change); err != nil {
		return ChangeRequest{}, err
	}
	return change, nil
}

// Accept marks final delivery and its user-approval DAG node completed only
// after explicit user action. Task metadata and runtime state are serialized so
// the completed screen cannot retain a stale READY acceptance node.
func (s *Service) Accept(ctx context.Context, id string) (Task, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if item.Status != StatusAcceptance {
		return Task{}, errors.New("task is not waiting for final acceptance")
	}
	state, runtimeErr := s.repository.LoadRuntime(id)
	if runtimeErr != nil {
		return Task{}, runtimeErr
	}
	if state != nil {
		if current, exists := state.NodeStates["acceptance"]; exists && current != "SUCCEEDED" {
			if !nodeTransitionAllowed(current, "SUCCEEDED") {
				return Task{}, fmt.Errorf("acceptance node transition %s -> SUCCEEDED is not allowed", current)
			}
			state.NodeStates["acceptance"] = "SUCCEEDED"
			state.Revision++
			state.UpdatedAt = time.Now().UTC()
			if err := s.repository.SaveRuntime(id, *state); err != nil {
				return Task{}, err
			}
		}
	}
	item.Status = StatusCompleted
	item.Phase = "COMPLETED"
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveTask(item); err != nil {
		return Task{}, err
	}
	return item, nil
}

// SetStatus changes the high-level task lifecycle for pause and resume controls.
func (s *Service) SetStatus(ctx context.Context, id string, status string) (Task, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if item.Status == StatusClosed {
		return Task{}, errors.New("closed task is read-only")
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != StatusReady && status != StatusPlanning && status != StatusPaused && status != StatusRunning && status != StatusAcceptance {
		return Task{}, errors.New("unsupported task status transition")
	}
	item.Status = status
	if status == StatusReady {
		item.Phase = PhasePlanning
	}
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveTask(item); err != nil {
		return Task{}, err
	}
	return item, nil
}

// AdvanceStage archives unfinished work in the current phase, unlocks eligible
// successors, and moves the task to the next workflow phase. The task remains
// PLANNING until the DEVELOPMENT boundary is reached.
func (s *Service) AdvanceStage(ctx context.Context, id string) (TaskDetail, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	if item.Status == StatusClosed {
		return TaskDetail{}, errors.New("closed task is read-only")
	}
	plan, err := s.repository.LoadPlan(id)
	if err != nil || plan == nil {
		return TaskDetail{}, errors.New("task plan is unavailable")
	}
	state, err := s.repository.LoadRuntime(id)
	if err != nil || state == nil {
		return TaskDetail{}, errors.New("task runtime is unavailable")
	}
	phases := orderedWorkflowPhases(plan.Workflow)
	currentIndex := -1
	for index, phase := range phases {
		if phase == item.Phase {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		return TaskDetail{}, errors.New("current task phase is not present in the workflow")
	}
	for _, node := range plan.Workflow {
		if node.Phase != item.Phase {
			continue
		}
		status := state.NodeStates[node.Key]
		if node.Type == "AGENT" && status != "SUCCEEDED" && status != "SKIPPED" {
			return TaskDetail{}, fmt.Errorf("阶段任务“%s”尚未完成，不能进入下一阶段", node.Name)
		}
		if status != "SUCCEEDED" && status != "SKIPPED" {
			state.NodeStates[node.Key] = "SUCCEEDED"
		}
	}
	for _, node := range plan.Workflow {
		if state.NodeStates[node.Key] == "PENDING" && dependenciesSatisfied(node, state.NodeStates) {
			state.NodeStates[node.Key] = "READY"
		}
	}
	now := time.Now().UTC()
	state.Revision++
	state.UpdatedAt = now
	if err := s.repository.SaveRuntime(id, *state); err != nil {
		return TaskDetail{}, err
	}
	if currentIndex+1 < len(phases) {
		item.Phase = phases[currentIndex+1]
		if workflowPhaseAtOrAfter(plan.Workflow, item.Phase, "DEVELOPMENT") {
			item.Status = StatusRunning
		} else {
			item.Status = StatusPlanning
		}
	} else {
		item.Phase = "DELIVERY"
		item.Status = StatusAcceptance
	}
	item.Revision++
	item.UpdatedAt = now
	if err := s.repository.SaveTask(item); err != nil {
		return TaskDetail{}, err
	}
	return s.Detail(ctx, id)
}

// SaveDelivery stores frontend and backend addresses shown during final acceptance.
func (s *Service) SaveDelivery(ctx context.Context, id string, delivery DeliveryInfo) (DeliveryInfo, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return DeliveryInfo{}, err
	}
	if item.Status == StatusClosed {
		return DeliveryInfo{}, errors.New("closed task is read-only")
	}
	delivery.FrontendURL = strings.TrimSpace(delivery.FrontendURL)
	delivery.BackendURL = strings.TrimSpace(delivery.BackendURL)
	delivery.Notes = strings.TrimSpace(delivery.Notes)
	delivery.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveDelivery(id, delivery); err != nil {
		return DeliveryInfo{}, err
	}
	return delivery, nil
}

// Deploy starts the generated backend and frontend for a completed task. Ports
// may be selected explicitly or allocated from localhost, and the resulting
// process ownership is persisted so later redeploy/delete operations stay safe.
func (s *Service) Deploy(ctx context.Context, id string, input DeployInput) (DeliveryInfo, error) {
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()

	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return DeliveryInfo{}, err
	}
	if item.Status != StatusCompleted {
		return DeliveryInfo{}, errors.New("only completed tasks can be deployed")
	}
	root, err := s.repository.Root(id)
	if err != nil {
		return DeliveryInfo{}, err
	}
	if err := stopPersistedDeployment(s.repository, id, false); err != nil {
		return DeliveryInfo{}, err
	}
	backendPort, err := resolveDeploymentPort(input.BackendPort)
	if err != nil {
		return DeliveryInfo{}, fmt.Errorf("backend port: %w", err)
	}
	frontendPort, err := resolveDeploymentPort(input.FrontendPort)
	if err != nil {
		return DeliveryInfo{}, fmt.Errorf("frontend port: %w", err)
	}
	if frontendPort == backendPort {
		return DeliveryInfo{}, errors.New("frontend and backend ports must be different")
	}

	backendPID, err := startTaskProjectProcess(root, "backend", backendPort, backendPort)
	if err != nil {
		return DeliveryInfo{}, fmt.Errorf("start backend: %w", err)
	}
	frontendPID, err := startTaskProjectProcess(root, "frontend", frontendPort, backendPort)
	if err != nil {
		stopDeploymentProcess(backendPID)
		return DeliveryInfo{}, fmt.Errorf("start frontend: %w", err)
	}
	if err := waitForDeploymentPort(backendPort, 8*time.Second); err != nil {
		stopDeploymentProcess(frontendPID)
		stopDeploymentProcess(backendPID)
		return DeliveryInfo{}, fmt.Errorf("backend did not become ready; inspect .cc-hardness/deployment/backend.log: %w", err)
	}
	if err := waitForDeploymentPort(frontendPort, 8*time.Second); err != nil {
		stopDeploymentProcess(frontendPID)
		stopDeploymentProcess(backendPID)
		return DeliveryInfo{}, fmt.Errorf("frontend did not become ready; inspect .cc-hardness/deployment/frontend.log: %w", err)
	}
	now := time.Now().UTC()
	delivery := DeliveryInfo{
		FrontendURL:  fmt.Sprintf("http://127.0.0.1:%d", frontendPort),
		BackendURL:   backendDeploymentURL(root, backendPort),
		FrontendPort: frontendPort,
		BackendPort:  backendPort,
		FrontendPID:  frontendPID,
		BackendPID:   backendPID,
		Status:       "RUNNING",
		StartedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repository.SaveDelivery(id, delivery); err != nil {
		stopDeploymentProcess(frontendPID)
		stopDeploymentProcess(backendPID)
		return DeliveryInfo{}, err
	}
	return delivery, nil
}

// StopDeployment stops both process trees recorded for a task and persists the
// terminal deployment state. It is called before task deletion as well as by
// future lifecycle operations that must release the selected ports.
func (s *Service) StopDeployment(id string) error {
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	return stopPersistedDeployment(s.repository, id, true)
}

// stopPersistedDeployment terminates only PIDs stored in the task-local
// delivery record. Missing or stale processes are treated as already stopped.
func stopPersistedDeployment(repository *Repository, id string, persist bool) error {
	delivery, err := repository.LoadDelivery(id)
	if err != nil || delivery == nil {
		return err
	}
	stopDeploymentProcess(delivery.FrontendPID)
	stopDeploymentProcess(delivery.BackendPID)
	if !persist {
		return nil
	}
	delivery.FrontendPID = 0
	delivery.BackendPID = 0
	delivery.Status = "STOPPED"
	delivery.UpdatedAt = time.Now().UTC()
	return repository.SaveDelivery(id, *delivery)
}

// resolveDeploymentPort validates a requested localhost port or asks the OS for
// an available ephemeral port. The listener is closed before process startup;
// strict-port flags still protect against the small allocation race.
func resolveDeploymentPort(requested int) (int, error) {
	if requested < 0 || requested > 65535 {
		return 0, errors.New("port must be between 1 and 65535, or 0 for random")
	}
	address := "127.0.0.1:0"
	if requested > 0 {
		address = net.JoinHostPort("127.0.0.1", strconv.Itoa(requested))
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return 0, errors.New("port is already in use")
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitForDeploymentPort prevents the UI from persisting clickable addresses
// before the generated service has actually bound its localhost listener.
func waitForDeploymentPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("startup timed out")
}

// startTaskProjectProcess starts one npm project with task-scoped environment
// variables and append-only logs. A waiter goroutine owns both process reaping
// and log-file closure for the complete child lifecycle.
func startTaskProjectProcess(taskRoot string, project string, port int, backendPort int) (int, error) {
	projectRoot := filepath.Join(taskRoot, project)
	if project == "backend" {
		if javaPID, javaProject, javaErr := startJavaBackendProcess(taskRoot, projectRoot, port); javaProject {
			return javaPID, javaErr
		}
	}
	packagePath := filepath.Join(projectRoot, "package.json")
	rawPackage, err := os.ReadFile(packagePath)
	if err != nil {
		return 0, fmt.Errorf("%s/package.json is unavailable: %w", project, err)
	}
	var packageMetadata struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(rawPackage, &packageMetadata); err != nil {
		return 0, fmt.Errorf("decode %s/package.json: %w", project, err)
	}
	script := "dev"
	if strings.TrimSpace(packageMetadata.Scripts[script]) == "" {
		script = "start"
	}
	scriptBody := strings.TrimSpace(packageMetadata.Scripts[script])
	if scriptBody == "" {
		return 0, fmt.Errorf("%s/package.json does not define dev or start", project)
	}

	npmCommand := "npm"
	if runtime.GOOS == "windows" {
		npmCommand = "npm.cmd"
	}
	args := []string{"run", script}
	if project == "frontend" && strings.Contains(strings.ToLower(scriptBody), "vite") {
		configPath, configErr := writeDeploymentViteConfig(taskRoot, port, backendPort)
		if configErr != nil {
			return 0, configErr
		}
		args = append(args, "--", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort", "--config", configPath)
	}
	command := exec.Command(npmCommand, args...)
	command.Dir = projectRoot
	command.Env = append(os.Environ(),
		"HOST=127.0.0.1",
		"PORT="+strconv.Itoa(port),
		strings.ToUpper(project)+"_PORT="+strconv.Itoa(port),
		"BACKEND_PORT="+strconv.Itoa(backendPort),
		fmt.Sprintf("VITE_API_BASE_URL=http://127.0.0.1:%d", backendPort),
		fmt.Sprintf("VITE_BACKEND_URL=http://127.0.0.1:%d", backendPort),
	)
	return startLoggedProjectCommand(command, taskRoot, project)
}

// startJavaBackendProcess recognizes Maven or Gradle Spring Boot projects and
// starts them with the selected server port. The boolean reports whether a Java
// project was detected so legacy npm backends can use the existing launcher.
func startJavaBackendProcess(taskRoot string, projectRoot string, port int) (int, bool, error) {
	var command *exec.Cmd
	switch {
	case fileExists(filepath.Join(projectRoot, "pom.xml")):
		executable := "mvn"
		if runtime.GOOS == "windows" {
			executable = "mvn.cmd"
		}
		if fileExists(filepath.Join(projectRoot, "mvnw.cmd")) && runtime.GOOS == "windows" {
			executable = filepath.Join(projectRoot, "mvnw.cmd")
		} else if fileExists(filepath.Join(projectRoot, "mvnw")) && runtime.GOOS != "windows" {
			executable = filepath.Join(projectRoot, "mvnw")
		}
		command = exec.Command(executable, "spring-boot:run", "-Dspring-boot.run.arguments=--server.port="+strconv.Itoa(port))
	case fileExists(filepath.Join(projectRoot, "build.gradle")) || fileExists(filepath.Join(projectRoot, "build.gradle.kts")):
		executable := "gradle"
		if runtime.GOOS == "windows" {
			executable = "gradle.bat"
		}
		if fileExists(filepath.Join(projectRoot, "gradlew.bat")) && runtime.GOOS == "windows" {
			executable = filepath.Join(projectRoot, "gradlew.bat")
		} else if fileExists(filepath.Join(projectRoot, "gradlew")) && runtime.GOOS != "windows" {
			executable = filepath.Join(projectRoot, "gradlew")
		}
		command = exec.Command(executable, "bootRun", "--args=--server.port="+strconv.Itoa(port))
	default:
		return 0, false, nil
	}
	command.Dir = projectRoot
	command.Env = append(os.Environ(),
		"HOST=127.0.0.1",
		"PORT="+strconv.Itoa(port),
		"SERVER_PORT="+strconv.Itoa(port),
		"BACKEND_PORT="+strconv.Itoa(port),
	)
	pid, err := startLoggedProjectCommand(command, taskRoot, "backend")
	return pid, true, err
}

// startLoggedProjectCommand attaches append-only task deployment logs and owns
// process reaping plus log closure for either Node or Java launchers.
func startLoggedProjectCommand(command *exec.Cmd, taskRoot string, project string) (int, error) {
	logDirectory := filepath.Join(taskRoot, ".cc-hardness", "deployment")
	if err := os.MkdirAll(logDirectory, 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(filepath.Join(logDirectory, project+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	go func() {
		_ = command.Wait()
		_ = logFile.Close()
	}()
	return command.Process.Pid, nil
}

// backendDeploymentURL points Spring Boot deployments directly at Swagger UI;
// legacy generated backends retain their root address for compatibility.
func backendDeploymentURL(taskRoot string, port int) string {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if fileExists(filepath.Join(taskRoot, "backend", "pom.xml")) ||
		fileExists(filepath.Join(taskRoot, "backend", "build.gradle")) ||
		fileExists(filepath.Join(taskRoot, "backend", "build.gradle.kts")) {
		return base + "/swagger-ui/index.html"
	}
	return base
}

// fileExists reports whether a regular file or launcher path is present.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeDeploymentViteConfig wraps an existing Vite config without modifying the
// generated project. It overrides both the selected frontend port and /api proxy
// target so a custom backend port remains usable.
func writeDeploymentViteConfig(taskRoot string, frontendPort int, backendPort int) (string, error) {
	frontendRoot := filepath.Join(taskRoot, "frontend")
	sourceName := ""
	for _, candidate := range []string{"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs"} {
		if _, err := os.Stat(filepath.Join(frontendRoot, candidate)); err == nil {
			sourceName = candidate
			break
		}
	}
	if sourceName == "" {
		return "", errors.New("frontend uses Vite but no vite.config file was found")
	}
	deploymentDirectory := filepath.Join(taskRoot, ".cc-hardness", "deployment")
	if err := os.MkdirAll(deploymentDirectory, 0o755); err != nil {
		return "", err
	}
	configPath := filepath.Join(deploymentDirectory, "vite.runtime.config.mjs")
	content := fmt.Sprintf(`// Generated by cc-hardness deployment; the project-owned config remains unchanged.
import originalConfig from "../../frontend/%s";
const frontendPort = %d;
const backendTarget = "http://127.0.0.1:%d";
export default async function runtimeConfig(environment) {
  const source = typeof originalConfig === "function" ? await originalConfig(environment) : originalConfig;
  const server = source?.server ?? {};
  const preview = source?.preview ?? {};
  const serverProxy = server.proxy ?? {};
  const previewProxy = preview.proxy ?? {};
  return {
    ...source,
    server: { ...server, host: "127.0.0.1", port: frontendPort, strictPort: true, proxy: { ...serverProxy, "/api": { ...(serverProxy["/api"] ?? {}), target: backendTarget } } },
    preview: { ...preview, host: "127.0.0.1", port: frontendPort, strictPort: true, proxy: { ...previewProxy, "/api": { ...(previewProxy["/api"] ?? {}), target: backendTarget } } },
  };
}
`, sourceName, frontendPort, backendPort)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return configPath, nil
}

// stopDeploymentProcess terminates a persisted process tree. Stale PIDs are
// harmless because deployment state may outlive a manual process shutdown.
func stopDeploymentProcess(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}

// RelatedTaskIDs scans task plans and returns references to a catalog asset.
func (s *Service) RelatedTaskIDs(ctx context.Context, kind string, assetID string) ([]string, error) {
	items, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, item := range items {
		plan, err := s.repository.LoadPlan(item.ID)
		if err != nil || plan == nil {
			continue
		}
		if planReferences(*plan, kind, assetID) {
			result = append(result, item.ID)
		}
	}
	return result, nil
}

// ConfigurationSetTaskIDs returns task plans that still reference a reusable
// configuration set so catalog deletion cannot create stale plan bindings.
func (s *Service) ConfigurationSetTaskIDs(ctx context.Context, configSetID string) ([]string, error) {
	items, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	taskIDs := make([]string, 0)
	for _, item := range items {
		plan, loadErr := s.repository.LoadPlan(item.ID)
		if loadErr != nil || plan == nil {
			continue
		}
		for _, id := range plan.ConfigSetIDs {
			if id == configSetID {
				taskIDs = append(taskIDs, item.ID)
				break
			}
		}
	}
	return taskIDs, nil
}

// Member returns one confirmed team member used to route a Claude session.
func (s *Service) Member(id string, memberID string) (TeamMember, error) {
	plan, err := s.repository.LoadPlan(id)
	if err != nil || plan == nil {
		return TeamMember{}, errors.New("task plan is unavailable")
	}
	for _, member := range plan.Team {
		if member.ID == memberID {
			return member, nil
		}
	}
	return TeamMember{}, errors.New("team member not found")
}

// Root returns the canonical workspace path for runtime process creation.
func (s *Service) Root(id string) (string, error) {
	return s.repository.Root(id)
}

// AgentPrompt returns the selected agent Markdown plus task coordination context.
func (s *Service) AgentPrompt(taskID string, memberID string, userMessage string) (string, error) {
	memberName := ""
	var agent catalog.Asset
	if strings.HasPrefix(memberID, "task-builder") {
		plan, err := s.repository.LoadPlan(taskID)
		if err != nil || plan == nil {
			return "", errors.New("task builder plan is unavailable")
		}
		agent, err = s.catalog.GetVersion(catalog.KindAgent, plan.BuilderAgentID, plan.BuilderAgentVersion)
		if err != nil {
			return "", err
		}
		memberName = "团队组建智能体"
	} else {
		member, err := s.Member(taskID, memberID)
		if err != nil {
			return "", err
		}
		agent, err = s.catalog.GetVersion(catalog.KindAgent, member.AgentID, member.AgentVersion)
		if err != nil {
			return "", err
		}
		memberName = member.Name
	}
	item, err := s.repository.Get(context.Background(), taskID)
	if err != nil {
		return "", err
	}
	root, err := s.repository.Root(taskID)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(memberID, "task-builder") {
		if err := s.materializeAgentSkills(root, agent); err != nil {
			return "", err
		}
	}
	portContext := fmt.Sprintf("前端服务使用预分配端口 %d，后端服务使用预分配端口 %d；不要要求用户填写交付地址。新建后端默认采用 Java 21 + Spring Boot 3.x + Maven，必须包含 springdoc Swagger UI 依赖并开放 /swagger-ui/index.html；仅当用户明确指定其他技术栈时才可偏离。", item.FrontendPort, item.BackendPort)
	builderContext := ""
	if strings.HasPrefix(memberID, "task-builder") {
		builderContext = "\n当前处于团队组建阶段。任务 ID 为 " + taskID + "。调整团队成员、成员版本/技能/规则/知识库、配置集、任务级资源或工作流时，禁止直接编辑 .cc-hardness/team-plan.json；必须先读取并遵循 .claude/skills/team-plan-api/SKILL.md，通过 http://127.0.0.1:8080/api/v1/tasks/" + taskID + "/plan/mutations 提交变更。可通过 GET http://127.0.0.1:8080/api/v1/config-sets 查询配置集。后端负责校验、原子写入、revision 和 updatedAt。"
	}
	configurationContext := ""
	if !strings.HasPrefix(memberID, "task-builder") {
		configurationContext = "\n任务绑定的配置集快照位于 .cc-hardness/runtime/config-sets.json。开发中需要 Redis、数据库、对象存储、消息队列等中间件时必须先按配置名称查询该文件：已有配置则按用户原文应用；没有配置则先询问用户是否需要该中间件及是否提供配置，禁止自行编造地址、端口或密码。"
	}
	artifactContext := ""
	if item.Phase != PhasePlanning && !strings.HasPrefix(memberID, "task-builder") {
		stageDirectory := strings.ToLower(strings.ReplaceAll(item.Phase, "_", "-"))
		agentDirectory := safeDocumentPathSegment(memberID)
		relativeArtifactDirectory := filepath.ToSlash(filepath.Join("docs", "agents", agentDirectory, stageDirectory))
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relativeArtifactDirectory)), 0o755); err != nil {
			return "", fmt.Errorf("create agent document directory: %w", err)
		}
		documentNames := expectedTechnicalDocumentNames(item.Name, item.Phase, memberID)
		artifactContext = "\n当前阶段为 " + item.Phase + "。所有正式技术文档统一保存到固定目录 " + relativeArtifactDirectory + "/；目录按生成智能体区分，禁止再把新文档写入 docs/stages。文档文件名必须使用“任务名称+XXX文档”的格式，例如 " + strings.Join(documentNames, "、") + "。同一类型有多份内容时应合并为一份主文档，截图、日志、源码和机器生成证据不受该文件名规则约束。"
	}
	return agent.Body + "\n\n## 当前任务上下文\n你在 cc-hardness 团队中以 " + memberName + " 身份工作。所有文件操作限制在当前工作目录。\n" + portContext + builderContext + configurationContext + artifactContext + "\n\n## 用户消息\n" + userMessage, nil
}

// expectedTechnicalDocumentNames gives each lifecycle phase a stable primary
// document name while preserving the user-visible task name as the prefix.
func expectedTechnicalDocumentNames(taskName string, phase string, memberID string) []string {
	prefix := safeDocumentPathSegment(taskName)
	documentTypes := map[string][]string{
		"ALIGNMENT":    {"PRD文档"},
		"ARCHITECTURE": {"概要设计文档", "详细设计文档"},
		"PROTOTYPE":    {"UI设计文档"},
		"DEVELOPMENT":  {"开发文档"},
		"INTEGRATION":  {"联调报告文档"},
		"QUALITY":      {"质量检测报告文档"},
		"TEST":         {"测试报告文档"},
		"DELIVERY":     {"交付文档"},
	}
	types := documentTypes[strings.ToUpper(strings.TrimSpace(phase))]
	if len(types) == 0 {
		types = []string{safeDocumentPathSegment(memberID) + "阶段文档"}
	}
	names := make([]string, 0, len(types))
	for _, documentType := range types {
		names = append(names, prefix+"+"+documentType+".md")
	}
	return names
}

// safeDocumentPathSegment removes Windows and Unix path separators plus reserved
// filename characters while retaining readable Chinese task and member names.
func safeDocumentPathSegment(value string) string {
	replacer := strings.NewReplacer(
		"<", "-", ">", "-", ":", "-", "\"", "-", "/", "-", "\\", "-",
		"|", "-", "?", "-", "*", "-",
	)
	result := strings.Trim(replacer.Replace(strings.TrimSpace(value)), " .")
	if result == "" {
		return "未命名任务"
	}
	return result
}

// materializeAgentSkills publishes the selected agent's current default skills
// into the task-local Claude directory before a planning or conversation turn.
func (s *Service) materializeAgentSkills(root string, agent catalog.Asset) error {
	for _, skillID := range agent.DefaultSkillIDs {
		skill, err := s.catalog.Get(catalog.KindSkill, skillID)
		if err != nil {
			return fmt.Errorf("agent skill %s is unavailable: %w", skillID, err)
		}
		if err := filedata.WriteText(filepath.Join(root, ".claude", "skills", skillID, "SKILL.md"), skill.Body); err != nil {
			return err
		}
	}
	return nil
}

// materializePlan writes immutable selected assets into Claude Code's task-local directories.
func (s *Service) materializePlan(root string, plan Plan) error {
	if plan.BuilderAgentID != "" && plan.BuilderAgentVersion != "" {
		builder, err := s.catalog.GetVersion(catalog.KindAgent, plan.BuilderAgentID, plan.BuilderAgentVersion)
		if err != nil {
			return err
		}
		if err := filedata.WriteText(filepath.Join(root, ".claude", "agents", "task-builder.md"), builder.Body); err != nil {
			return err
		}
	}
	for _, member := range plan.Team {
		agent, err := s.catalog.GetVersion(catalog.KindAgent, member.AgentID, member.AgentVersion)
		if err != nil {
			return err
		}
		if err := filedata.WriteText(filepath.Join(root, ".claude", "agents", member.ID+".md"), agent.Body); err != nil {
			return err
		}
		for _, skillID := range member.SkillIDs {
			skill, err := s.catalog.Get(catalog.KindSkill, skillID)
			if err != nil {
				return err
			}
			if err := filedata.WriteText(filepath.Join(root, ".claude", "skills", skillID, "SKILL.md"), skill.Body); err != nil {
				return err
			}
		}
		for _, ruleID := range member.RuleIDs {
			rule, err := s.catalog.Get(catalog.KindRule, ruleID)
			if err != nil {
				return err
			}
			memberRule := "# 仅对成员 " + member.Name + " 生效\n\n" + rule.Body
			if err := filedata.WriteText(filepath.Join(root, ".claude", "rules", "member-"+member.ID+"-"+ruleID+".md"), memberRule); err != nil {
				return err
			}
		}
	}
	for _, ruleID := range plan.TaskRuleIDs {
		rule, err := s.catalog.Get(catalog.KindRule, ruleID)
		if err != nil {
			return err
		}
		if err := filedata.WriteText(filepath.Join(root, ".claude", "rules", ruleID+".md"), rule.Body); err != nil {
			return err
		}
	}
	if err := filedata.WriteJSON(filepath.Join(root, ".cc-hardness", "team.json"), plan.Team); err != nil {
		return err
	}
	taskKnowledge := make([]catalog.Asset, 0, len(plan.TaskKnowledgeIDs))
	for _, knowledgeID := range plan.TaskKnowledgeIDs {
		item, err := s.catalog.Get(catalog.KindKnowledge, knowledgeID)
		if err != nil {
			return err
		}
		taskKnowledge = append(taskKnowledge, item)
	}
	knowledgeManifest := map[string]any{"task": taskKnowledge, "members": map[string][]catalog.Asset{}}
	memberKnowledge := knowledgeManifest["members"].(map[string][]catalog.Asset)
	for _, member := range plan.Team {
		items := make([]catalog.Asset, 0, len(member.KnowledgeIDs))
		for _, knowledgeID := range member.KnowledgeIDs {
			item, err := s.catalog.Get(catalog.KindKnowledge, knowledgeID)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		memberKnowledge[member.ID] = items
	}
	if err := filedata.WriteJSON(filepath.Join(root, ".cc-hardness", "runtime", "knowledge.json"), knowledgeManifest); err != nil {
		return err
	}
	configurationSets, err := s.configSets.Resolve(plan.ConfigSetIDs)
	if err != nil {
		return err
	}
	if err := filedata.WriteJSON(filepath.Join(root, ".cc-hardness", "runtime", "config-sets.json"), configurationSets); err != nil {
		return err
	}
	return filedata.WriteJSON(filepath.Join(root, ".cc-hardness", "workflow", "definition.json"), plan.Workflow)
}

// validatePlan enforces project-manager uniqueness, references, and DAG acyclicity.
func validatePlan(plan Plan) error {
	memberIDs := make(map[string]bool)
	projectManagers := 0
	for _, member := range plan.Team {
		if member.ID == "" || member.AgentID == "" || memberIDs[member.ID] {
			return errors.New("team member ids and agent references must be unique and non-empty")
		}
		memberIDs[member.ID] = true
		if member.ID == "project-manager" && member.Role != "PROJECT_MANAGER" {
			return errors.New("project-manager member must use the PROJECT_MANAGER role")
		}
		if member.Role == "PROJECT_MANAGER" {
			projectManagers++
			if member.ID != "project-manager" {
				return errors.New("project manager must keep the project-manager member id")
			}
			if !member.Required {
				return errors.New("project manager must be required")
			}
		}
	}
	if projectManagers != 1 {
		return errors.New("team must contain exactly one project manager")
	}
	if !memberIDs["project-manager"] {
		return errors.New("required project manager member id is missing")
	}
	if len(plan.Workflow) == 0 {
		return errors.New("workflow must contain at least one node")
	}
	nodes := make(map[string]WorkflowNode)
	for _, node := range plan.Workflow {
		if node.Key == "" || nodes[node.Key].Key != "" {
			return errors.New("workflow node keys must be unique and non-empty")
		}
		if node.Type == "AGENT" && !memberIDs[node.AssigneeID] {
			return fmt.Errorf("node %s references an unknown member", node.Key)
		}
		if node.Type != "AGENT" && node.Type != "USER_APPROVAL" {
			return fmt.Errorf("node %s uses unsupported type %s", node.Key, node.Type)
		}
		if node.Background && node.Type != "AGENT" {
			return fmt.Errorf("background node %s must be assigned to an agent", node.Key)
		}
		if node.DependencyMode != "ALL" && node.DependencyMode != "ANY" {
			return fmt.Errorf("node %s uses unsupported dependency mode", node.Key)
		}
		if strings.TrimSpace(node.Phase) == "" {
			return fmt.Errorf("node %s phase is required", node.Key)
		}
		nodes[node.Key] = node
	}
	for _, node := range plan.Workflow {
		for _, dependency := range node.WaitFor {
			if _, exists := nodes[dependency]; !exists {
				return fmt.Errorf("node %s references missing dependency %s", node.Key, dependency)
			}
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return errors.New("workflow must be acyclic")
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dependency := range nodes[key].WaitFor {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range nodes {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

// validatePlanAssets verifies every frozen agent version and referenced capability.
// Disabled assets remain valid for existing task snapshots; only physical absence fails.
func (s *Service) validatePlanAssets(plan Plan) error {
	if plan.BuilderAgentID == "" || plan.BuilderAgentVersion == "" {
		return errors.New("task builder binding is required")
	}
	if _, err := s.catalog.GetVersion(catalog.KindAgent, plan.BuilderAgentID, plan.BuilderAgentVersion); err != nil {
		return fmt.Errorf("task builder version is unavailable: %w", err)
	}
	checked := map[string]bool{}
	checkCurrent := func(kind string, id string) error {
		key := kind + "\x00" + id
		if checked[key] {
			return nil
		}
		if _, err := s.catalog.Get(kind, id); err != nil {
			return fmt.Errorf("referenced %s asset %s is unavailable: %w", kind, id, err)
		}
		checked[key] = true
		return nil
	}
	for _, member := range plan.Team {
		if _, err := s.catalog.GetVersion(catalog.KindAgent, member.AgentID, member.AgentVersion); err != nil {
			return fmt.Errorf("agent version %s@%s is unavailable: %w", member.AgentID, member.AgentVersion, err)
		}
		for _, skillID := range member.SkillIDs {
			if err := checkCurrent(catalog.KindSkill, skillID); err != nil {
				return err
			}
		}
		for _, ruleID := range member.RuleIDs {
			if err := checkCurrent(catalog.KindRule, ruleID); err != nil {
				return err
			}
		}
		for _, knowledgeID := range member.KnowledgeIDs {
			if err := checkCurrent(catalog.KindKnowledge, knowledgeID); err != nil {
				return err
			}
		}
	}
	for _, ruleID := range plan.TaskRuleIDs {
		if err := checkCurrent(catalog.KindRule, ruleID); err != nil {
			return err
		}
	}
	for _, knowledgeID := range plan.TaskKnowledgeIDs {
		if err := checkCurrent(catalog.KindKnowledge, knowledgeID); err != nil {
			return err
		}
	}
	if _, err := s.configSets.Resolve(plan.ConfigSetIDs); err != nil {
		return err
	}
	return nil
}

// dependenciesSatisfied evaluates ALL or ANY dependency mode against runtime states.
func dependenciesSatisfied(node WorkflowNode, states map[string]string) bool {
	if node.DependencyMode == "ANY" {
		for _, key := range node.WaitFor {
			if states[key] == "SUCCEEDED" || states[key] == "SKIPPED" {
				return true
			}
		}
		return false
	}
	for _, key := range node.WaitFor {
		if states[key] != "SUCCEEDED" && states[key] != "SKIPPED" {
			return false
		}
	}
	return true
}

// nodeTransitionAllowed protects DAG execution from arbitrary or stale UI transitions.
// READY and FAILED nodes may be retried, active nodes may finish, and terminal nodes
// remain immutable except for idempotent requests caused by repeated local clicks.
func nodeTransitionAllowed(current string, next string) bool {
	if current == next {
		return true
	}
	allowed := map[string]map[string]bool{
		"PENDING":      {"READY": true, "SKIPPED": true},
		"READY":        {"RUNNING": true, "WAITING_USER": true, "SKIPPED": true},
		"RUNNING":      {"SUCCEEDED": true, "FAILED": true},
		"WAITING_USER": {"SUCCEEDED": true, "FAILED": true},
		"FAILED":       {"RUNNING": true, "WAITING_USER": true, "SKIPPED": true},
	}
	return allowed[current][next]
}

// normalizePlanCollections keeps JSON arrays stable for the React client and old task files.
// Go encodes nil slices as null, while capability toggles and DAG rendering require arrays.
func normalizePlanCollections(plan *Plan) {
	if plan.TaskRuleIDs == nil {
		plan.TaskRuleIDs = []string{}
	}
	if plan.TaskKnowledgeIDs == nil {
		plan.TaskKnowledgeIDs = []string{}
	}
	if plan.ConfigSetIDs == nil {
		plan.ConfigSetIDs = []string{}
	}
	for index := range plan.Team {
		if plan.Team[index].SkillIDs == nil {
			plan.Team[index].SkillIDs = []string{}
		}
		if plan.Team[index].RuleIDs == nil {
			plan.Team[index].RuleIDs = []string{}
		}
		if plan.Team[index].KnowledgeIDs == nil {
			plan.Team[index].KnowledgeIDs = []string{}
		}
	}
	for index := range plan.Workflow {
		if plan.Workflow[index].WaitFor == nil {
			plan.Workflow[index].WaitFor = []string{}
		}
	}
}

// normalizeMemberCollections keeps one member payload safe for JSON encoding and
// capability toggles before it is appended or replaced.
func normalizeMemberCollections(member *TeamMember) {
	if member.SkillIDs == nil {
		member.SkillIDs = []string{}
	}
	if member.RuleIDs == nil {
		member.RuleIDs = []string{}
	}
	if member.KnowledgeIDs == nil {
		member.KnowledgeIDs = []string{}
	}
}

// safeAvailableMemberID derives a stable task member ID from trusted catalog
// metadata and adds a numeric suffix when the role already exists.
func safeAvailableMemberID(team []TeamMember, role string, agentID string) string {
	base := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(role), "_", "-"))
	if !validMemberID(base) {
		base = strings.ToLower(strings.TrimSpace(agentID))
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		available := true
		for _, member := range team {
			if member.ID == candidate {
				available = false
				break
			}
		}
		if available {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
}

// validMemberID limits identifiers that later become task-local filenames.
func validMemberID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

// insertMemberWorkflow creates a conservative DAG branch for direct UI additions.
// Agents may supply a complete workflow in the same API call for semantic placement.
func insertMemberWorkflow(workflow []WorkflowNode, member TeamMember) []WorkflowNode {
	taskKey := member.ID + "-work"
	lowerRole := strings.ToLower(member.Role + " " + member.Name)
	phase := "DEVELOPMENT"
	dependencies := workflowKeysByPhase(workflow, "ARCHITECTURE")
	downstreamPhases := map[string]bool{"INTEGRATION": true}
	description := "由" + member.Name + "完成该角色在项目开发阶段的专业工作。"

	if strings.Contains(lowerRole, "test") || strings.Contains(lowerRole, "测试") || strings.Contains(lowerRole, "quality") || strings.Contains(lowerRole, "质量") {
		phase = "TEST"
		dependencies = workflowKeysByPhase(workflow, "INTEGRATION")
		if len(dependencies) == 0 {
			dependencies = workflowKeysByPhase(workflow, "DEVELOPMENT")
		}
		downstreamPhases = map[string]bool{"DELIVERY": true}
		description = "在开发和集成完成后执行测试或质量检查，并提交可追踪结论。"
	} else if strings.Contains(lowerRole, "auditor") || strings.Contains(lowerRole, "审计") || strings.Contains(lowerRole, "交付") || strings.Contains(lowerRole, "集成") {
		phase = "INTEGRATION"
		dependencies = workflowKeysByPhase(workflow, "DEVELOPMENT")
		downstreamPhases = map[string]bool{"TEST": true, "DELIVERY": true}
		description = "在开发完成后执行集成、审计与交付准备。"
	}
	node := WorkflowNode{
		Key: taskKey, Name: member.Name + "任务", Type: "AGENT", AssigneeID: member.ID,
		Phase: phase, WaitFor: dependencies, DependencyMode: "ALL", Status: "PENDING",
		Description: description,
	}
	insertAt := len(workflow)
	for index, existing := range workflow {
		if downstreamPhases[existing.Phase] {
			insertAt = index
			break
		}
	}
	result := append([]WorkflowNode{}, workflow...)
	result = append(result, WorkflowNode{})
	copy(result[insertAt+1:], result[insertAt:])
	result[insertAt] = node
	for index := range result {
		if downstreamPhases[result[index].Phase] && result[index].Key != taskKey {
			result[index].WaitFor = appendUnique(result[index].WaitFor, taskKey)
		}
	}
	return result
}

// workflowKeysByPhase returns stable predecessor keys for one generated branch.
func workflowKeysByPhase(workflow []WorkflowNode, phase string) []string {
	keys := make([]string, 0)
	for _, node := range workflow {
		if node.Phase == phase && node.Type == "AGENT" {
			keys = append(keys, node.Key)
		}
	}
	return keys
}

// appendUnique adds one dependency while preserving its existing order.
func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// filterMembers removes exactly one member from a copied team slice.
func filterMembers(team []TeamMember, memberID string) []TeamMember {
	result := make([]TeamMember, 0, len(team)-1)
	for _, member := range team {
		if member.ID != memberID {
			result = append(result, member)
		}
	}
	return result
}

// repairWorkflowAfterMemberRemoval removes owned nodes and recursively replaces
// their downstream references with the nearest surviving predecessors.
func repairWorkflowAfterMemberRemoval(workflow []WorkflowNode, memberID string) []WorkflowNode {
	removed := make(map[string]WorkflowNode)
	for _, node := range workflow {
		if node.AssigneeID == memberID {
			removed[node.Key] = node
		}
	}
	var expand func(string, map[string]bool) []string
	expand = func(key string, visiting map[string]bool) []string {
		node, exists := removed[key]
		if !exists || visiting[key] {
			if visiting[key] {
				return []string{}
			}
			return []string{key}
		}
		nextVisiting := make(map[string]bool, len(visiting)+1)
		for item := range visiting {
			nextVisiting[item] = true
		}
		nextVisiting[key] = true
		result := make([]string, 0)
		for _, dependency := range node.WaitFor {
			for _, expanded := range expand(dependency, nextVisiting) {
				result = appendUnique(result, expanded)
			}
		}
		return result
	}
	result := make([]WorkflowNode, 0, len(workflow)-len(removed))
	for _, node := range workflow {
		if _, exists := removed[node.Key]; exists {
			continue
		}
		dependencies := make([]string, 0)
		for _, dependency := range node.WaitFor {
			for _, expanded := range expand(dependency, map[string]bool{}) {
				dependencies = appendUnique(dependencies, expanded)
			}
		}
		node.WaitFor = dependencies
		result = append(result, node)
	}
	return result
}

// planningInputFiles returns stable relative paths for task-builder inventory analysis.
// File contents remain in the workspace; the planning snapshot records only the bounded
// directory structure so binary design assets and large documents are not duplicated.
func planningInputFiles(root string) ([]string, error) {
	inputRoot := filepath.Join(root, "inputs")
	if _, err := os.Stat(inputRoot); errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	} else if err != nil {
		return nil, err
	}
	items := make([]string, 0)
	err := filepath.WalkDir(inputRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		items = append(items, filepath.ToSlash(relative))
		if len(items) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})
	return items, err
}

// planReferences checks whether a task plan binds a catalog asset.
func planReferences(plan Plan, kind string, assetID string) bool {
	for _, member := range plan.Team {
		if kind == catalog.KindAgent && member.AgentID == assetID {
			return true
		}
		if kind == catalog.KindSkill && contains(member.SkillIDs, assetID) {
			return true
		}
		if kind == catalog.KindRule && (contains(member.RuleIDs, assetID) || contains(plan.TaskRuleIDs, assetID)) {
			return true
		}
		if kind == catalog.KindKnowledge && (contains(member.KnowledgeIDs, assetID) || contains(plan.TaskKnowledgeIDs, assetID)) {
			return true
		}
	}
	return false
}

// contains reports whether a string list contains value.
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// defaultWorkflow returns the initial editable project-delivery DAG.
func defaultWorkflow() []WorkflowNode {
	return []WorkflowNode{
		{Key: "intent-alignment", Name: "需求分析与意图对齐", Type: "AGENT", AssigneeID: "product-manager", Phase: "ALIGNMENT", DependencyMode: "ALL", Status: "PENDING", Description: "分析已有材料，与用户完善并输出 PRD。"},
		{Key: "prd-approval", Name: "确认 PRD", Type: "USER_APPROVAL", Phase: "ALIGNMENT", WaitFor: []string{"intent-alignment"}, DependencyMode: "ALL", Status: "PENDING", Description: "用户确认需求基线。"},
		{Key: "architecture-contract", Name: "接口契约预设计", Type: "AGENT", AssigneeID: "architect", Phase: "ARCHITECTURE", Background: true, WaitFor: []string{"intent-alignment"}, DependencyMode: "ALL", Status: "PENDING", Description: "基于 PRD 初稿先输出领域边界、API 与数据契约草案，供前后端搭建可回退骨架。"},
		{Key: "architecture", Name: "详细架构与业务设计", Type: "AGENT", AssigneeID: "architect", Phase: "ARCHITECTURE", WaitFor: []string{"prd-approval", "architecture-contract"}, DependencyMode: "ALL", Status: "PENDING", Description: "根据确认 PRD 完成详细业务、数据、接口和部署设计，并校正预设计契约。"},
		{Key: "architecture-approval", Name: "确认技术设计", Type: "USER_APPROVAL", Phase: "ARCHITECTURE", WaitFor: []string{"architecture"}, DependencyMode: "ALL", Status: "PENDING", Description: "用户确认技术基线。"},
		{Key: "prototype", Name: "原型设计", Type: "AGENT", AssigneeID: "ui-designer", Phase: "PROTOTYPE", WaitFor: []string{"architecture-approval"}, DependencyMode: "ALL", Status: "PENDING", Description: "调用 Stitch 或输出设计系统与原型。"},
		{Key: "backend-foundation", Name: "后端接口骨架", Type: "AGENT", AssigneeID: "backend-engineer", Phase: "DEVELOPMENT", Background: true, WaitFor: []string{"architecture-contract"}, DependencyMode: "ALL", Status: "PENDING", Description: "只搭建 Spring Boot、Swagger、分层目录、领域类型和接口桩，不实现尚未确认的具体业务流程。"},
		{Key: "frontend-foundation", Name: "前端功能骨架", Type: "AGENT", AssigneeID: "frontend-engineer", Phase: "DEVELOPMENT", Background: true, WaitFor: []string{"architecture-contract"}, DependencyMode: "ALL", Status: "PENDING", Description: "只实现类型、API 客户端、状态、路由和页面壳，不固化尚未确认的业务和视觉细节。"},
		{Key: "backend-development", Name: "后端业务开发", Type: "AGENT", AssigneeID: "backend-engineer", Phase: "DEVELOPMENT", WaitFor: []string{"architecture-approval", "backend-foundation"}, DependencyMode: "ALL", Status: "PENDING", Description: "基于确认的详细架构实现业务流程、持久化、权限和异常处理。"},
		{Key: "prototype-approval", Name: "确认原型", Type: "USER_APPROVAL", Phase: "PROTOTYPE", WaitFor: []string{"prototype"}, DependencyMode: "ALL", Status: "PENDING", Description: "用户确认页面和视觉。"},
		{Key: "frontend-ui", Name: "前端界面开发", Type: "AGENT", AssigneeID: "frontend-engineer", Phase: "DEVELOPMENT", WaitFor: []string{"frontend-foundation", "prototype-approval"}, DependencyMode: "ALL", Status: "PENDING", Description: "根据确认原型实现完整界面。"},
		{Key: "integration", Name: "前后端联调", Type: "AGENT", AssigneeID: "auditor", Phase: "INTEGRATION", WaitFor: []string{"backend-development", "frontend-ui"}, DependencyMode: "ALL", Status: "PENDING", Description: "启动服务并检查契约和核心流程。"},
		{Key: "testing", Name: "专业测试", Type: "AGENT", AssigneeID: "test-engineer", Phase: "TEST", WaitFor: []string{"integration"}, DependencyMode: "ALL", Status: "PENDING", Description: "生成测试文档并执行 API 与浏览器测试。"},
		{Key: "delivery", Name: "本地交付", Type: "AGENT", AssigneeID: "auditor", Phase: "DELIVERY", WaitFor: []string{"testing"}, DependencyMode: "ALL", Status: "PENDING", Description: "启动项目并生成交付说明。"},
		{Key: "acceptance", Name: "最终验收", Type: "USER_APPROVAL", Phase: "DELIVERY", WaitFor: []string{"delivery"}, DependencyMode: "ALL", Status: "PENDING", Description: "用户确认交付或提出增量修改。"},
	}
}

// orderedWorkflowPhases returns unique phases in execution-definition order.
// Dynamic phases introduced by task-builder edits are therefore advanced without
// requiring a hard-coded server migration.
func orderedWorkflowPhases(workflow []WorkflowNode) []string {
	seen := map[string]bool{}
	phases := make([]string, 0)
	for _, node := range workflow {
		if node.Phase == "" || seen[node.Phase] {
			continue
		}
		seen[node.Phase] = true
		phases = append(phases, node.Phase)
	}
	return phases
}

// workflowPhaseAtOrAfter compares phases using their first appearance in the DAG.
func workflowPhaseAtOrAfter(workflow []WorkflowNode, phase string, boundary string) bool {
	phases := orderedWorkflowPhases(workflow)
	phaseIndex := -1
	boundaryIndex := -1
	for index, value := range phases {
		if value == phase {
			phaseIndex = index
		}
		if value == boundary {
			boundaryIndex = index
		}
	}
	return phaseIndex >= 0 && boundaryIndex >= 0 && phaseIndex >= boundaryIndex
}
