// Package httpapi exposes the cc-hardness local REST API and production Web assets.
package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	gort "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"cc-hardness/internal/catalog"
	"cc-hardness/internal/configset"
	ccruntime "cc-hardness/internal/runtime"
	"cc-hardness/internal/task"
)

// Handler routes catalog, task, workflow, runtime, and static Web requests.
type Handler struct {
	// catalog contains versioned system and user assets.
	catalog *catalog.Service
	// configSets contains reusable environment and middleware configuration documents.
	configSets *configset.Service
	// tasks contains task planning and file-workspace behavior.
	tasks *task.Service
	// runtime owns active Claude Code child processes.
	runtime *ccruntime.Manager
	// webRoot is the compiled frontend directory used outside Vite development mode.
	webRoot string
	// logger records request failures and important state changes.
	logger *slog.Logger
	// mux uses Go path variables for the complete local REST surface.
	mux *http.ServeMux
}

// NewHandler creates the API router and production single-page application fallback.
func NewHandler(catalogService *catalog.Service, configSetService *configset.Service, taskService *task.Service, runtimeManager *ccruntime.Manager, webRoot string, logger *slog.Logger) *Handler {
	handler := &Handler{catalog: catalogService, configSets: configSetService, tasks: taskService, runtime: runtimeManager, webRoot: webRoot, logger: logger, mux: http.NewServeMux()}
	handler.registerRoutes()
	return handler
}

// registerRoutes binds every clickable Web action to a concrete API endpoint.
func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /api/v1/health", h.handleHealth)
	h.mux.HandleFunc("GET /api/v1/system/claude-info", h.handleClaudeInfo)
	h.mux.HandleFunc("GET /api/v1/assets/{kind}", h.handleListAssets)
	h.mux.HandleFunc("POST /api/v1/assets/{kind}", h.handleCreateAsset)
	h.mux.HandleFunc("GET /api/v1/assets/{kind}/{id}", h.handleGetAsset)
	h.mux.HandleFunc("GET /api/v1/assets/{kind}/{id}/content.md", h.handleAssetMarkdown)
	h.mux.HandleFunc("PUT /api/v1/assets/{kind}/{id}", h.handleUpdateAsset)
	h.mux.HandleFunc("DELETE /api/v1/assets/{kind}/{id}", h.handleDeleteAsset)
	h.mux.HandleFunc("POST /api/v1/assets/{kind}/{id}/versions", h.handleAddAssetVersion)
	h.mux.HandleFunc("GET /api/v1/assets/{kind}/{id}/relations", h.handleAssetRelations)
	h.mux.HandleFunc("GET /api/v1/config-sets", h.handleListConfigSets)
	h.mux.HandleFunc("POST /api/v1/config-sets", h.handleCreateConfigSet)
	h.mux.HandleFunc("PUT /api/v1/config-sets/{id}", h.handleUpdateConfigSet)
	h.mux.HandleFunc("DELETE /api/v1/config-sets/{id}", h.handleDeleteConfigSet)

	h.mux.HandleFunc("GET /api/v1/tasks", h.handleListTasks)
	h.mux.HandleFunc("GET /api/v1/task-groups", h.handleListTaskGroups)
	h.mux.HandleFunc("POST /api/v1/tasks", h.handleCreateTask)
	h.mux.HandleFunc("GET /api/v1/tasks/{id}", h.handleGetTask)
	h.mux.HandleFunc("DELETE /api/v1/tasks/{id}", h.handleDeleteTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/close", h.handleCloseTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/plan", h.handleGeneratePlan)
	h.mux.HandleFunc("PUT /api/v1/tasks/{id}/plan", h.handleSavePlan)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/plan/mutations", h.handlePlanMutation)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/start", h.handleStartTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/advance", h.handleAdvanceTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/pause", h.handlePauseTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/resume", h.handleResumeTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/accept", h.handleAcceptTask)
	h.mux.HandleFunc("PUT /api/v1/tasks/{id}/delivery", h.handleSaveDelivery)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/deploy", h.handleDeployTask)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/changes", h.handleCreateChange)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/nodes/{nodeKey}/status", h.handleNodeStatus)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/nodes/{nodeKey}/run", h.handleRunNode)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/messages", h.handleTaskMessage)
	h.mux.HandleFunc("GET /api/v1/tasks/{id}/files", h.handleListTaskFiles)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/files", h.handleUploadTaskFile)
	h.mux.HandleFunc("POST /api/v1/tasks/{id}/files/open", h.handleOpenTaskFile)
	h.mux.HandleFunc("GET /api/v1/tasks/{id}/sessions", h.handleListTaskSessions)
	h.mux.HandleFunc("GET /api/v1/tasks/{id}/sessions/{sessionId}/events", h.handleTaskSessionEvents)

	h.mux.HandleFunc("GET /api/v1/sessions", h.handleListSessions)
	h.mux.HandleFunc("GET /api/v1/sessions/{id}", h.handleGetSession)
	h.mux.HandleFunc("GET /api/v1/sessions/{id}/events", h.handleSessionEvents)
	h.mux.HandleFunc("POST /api/v1/sessions/{id}/messages", h.handleSessionMessage)
	h.mux.HandleFunc("POST /api/v1/sessions/{id}/permissions", h.handlePermissionDecision)
	h.mux.HandleFunc("POST /api/v1/sessions/{id}/interrupt", h.handleInterruptSession)
	h.mux.HandleFunc("POST /api/v1/sessions/{id}/stop", h.handleStopSession)
	h.mux.HandleFunc("GET /", h.serveWeb)
}

// handleListConfigSets returns reusable local configuration collections.
func (h *Handler) handleListConfigSets(response http.ResponseWriter, _ *http.Request) {
	sets, err := h.configSets.List()
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": sets})
}

// handleCreateConfigSet validates and persists one configuration collection.
func (h *Handler) handleCreateConfigSet(response http.ResponseWriter, request *http.Request) {
	var input configset.UpsertInput
	if !decodeJSON(response, request, 2*1024*1024, &input) {
		return
	}
	set, err := h.configSets.Create(input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("configuration set created", "config_set_id", set.ID, "config_set_name", set.Name)
	writeJSON(response, http.StatusCreated, set)
}

// handleUpdateConfigSet validates and replaces one existing collection.
func (h *Handler) handleUpdateConfigSet(response http.ResponseWriter, request *http.Request) {
	var input configset.UpsertInput
	if !decodeJSON(response, request, 2*1024*1024, &input) {
		return
	}
	set, err := h.configSets.Update(request.PathValue("id"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("configuration set updated", "config_set_id", set.ID, "revision", set.Revision)
	writeJSON(response, http.StatusOK, set)
}

// handleDeleteConfigSet removes an unreferenced collection while preserving task plans.
func (h *Handler) handleDeleteConfigSet(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	taskIDs, err := h.tasks.ConfigurationSetTaskIDs(request.Context(), id)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if len(taskIDs) > 0 {
		writeServiceError(response, errors.New("configuration set is referenced by tasks: "+strings.Join(taskIDs, ", ")))
		return
	}
	if err := h.configSets.Delete(id); err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("configuration set deleted", "config_set_id", id)
	response.WriteHeader(http.StatusNoContent)
}

// ServeHTTP applies common security headers before routing the request.
func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "same-origin")
	response.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(response, request)
}

// handleHealth reports local service and Claude CLI availability.
func (h *Handler) handleHealth(response http.ResponseWriter, _ *http.Request) {
	_, claudeErr := os.Stat(filepath.Join(os.Getenv("APPDATA"), "npm", "claude.cmd"))
	writeJSON(response, http.StatusOK, map[string]any{"status": "ok", "claudeDetected": claudeErr == nil || commandExists("claude")})
}

// claudeInfo is the safe, non-secret subset of the local Claude Code runtime
// configuration shown in the workspace information dialog.
type claudeInfo struct {
	Detected       bool   `json:"detected"`
	Version        string `json:"version"`
	Model          string `json:"model"`
	ModelSource    string `json:"modelSource"`
	PermissionMode string `json:"permissionMode"`
	InputFormat    string `json:"inputFormat"`
	OutputFormat   string `json:"outputFormat"`
}

// handleClaudeInfo reports the installed Claude Code version and effective
// model selection without returning API keys, base URLs, or unrelated settings.
func (h *Handler) handleClaudeInfo(response http.ResponseWriter, request *http.Request) {
	info := readClaudeInfo(request.Context())
	writeJSON(response, http.StatusOK, info)
}

// readClaudeInfo combines the CLI version with the model setting inherited by
// child sessions. Process environment takes priority over Claude user settings.
func readClaudeInfo(parent context.Context) claudeInfo {
	info := claudeInfo{
		Model:          "Claude Code 默认模型",
		ModelSource:    "Claude Code 默认配置",
		PermissionMode: "bypassPermissions",
		InputFormat:    "stream-json",
		OutputFormat:   "stream-json",
	}

	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	var versionCommand *exec.Cmd
	if gort.GOOS == "windows" {
		versionCommand = exec.CommandContext(ctx, "cmd", "/C", "claude", "--version")
	} else {
		versionCommand = exec.CommandContext(ctx, "claude", "--version")
	}
	if output, err := versionCommand.CombinedOutput(); err == nil {
		info.Detected = true
		info.Version = strings.TrimSpace(string(output))
	}

	if model := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); model != "" {
		info.Model = model
		info.ModelSource = "进程环境变量 ANTHROPIC_MODEL"
		return info
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return info
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return info
	}
	var settings struct {
		Model string         `json:"model"`
		Env   map[string]any `json:"env"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return info
	}
	if model := strings.TrimSpace(settings.Model); model != "" {
		info.Model = model
		info.ModelSource = "Claude Code 用户配置 · model"
		return info
	}
	modelFromEnv, _ := settings.Env["ANTHROPIC_MODEL"].(string)
	if model := strings.TrimSpace(modelFromEnv); model != "" {
		info.Model = model
		info.ModelSource = "Claude Code 用户配置 · ANTHROPIC_MODEL"
	}
	return info
}

// handleListAssets returns the requested catalog kind.
func (h *Handler) handleListAssets(response http.ResponseWriter, request *http.Request) {
	items, err := h.catalog.List(request.PathValue("kind"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// handleGetAsset returns one current asset version.
func (h *Handler) handleGetAsset(response http.ResponseWriter, request *http.Request) {
	item, err := h.catalog.Get(request.PathValue("kind"), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, item)
}

// handleAssetMarkdown opens the selected immutable Markdown body in a dedicated browser tab.
// The route uses the catalog service for path validation instead of exposing arbitrary files.
func (h *Handler) handleAssetMarkdown(response http.ResponseWriter, request *http.Request) {
	item, err := h.catalog.Get(request.PathValue("kind"), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	response.Header().Set("Content-Disposition", `inline; filename="`+item.ID+"-"+item.CurrentVersion+`.md"`)
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte(item.Body))
}

// handleCreateAsset creates a user-owned agent, skill, rule, or knowledge source.
func (h *Handler) handleCreateAsset(response http.ResponseWriter, request *http.Request) {
	var input catalog.UpsertInput
	if !decodeJSON(response, request, 512*1024, &input) {
		return
	}
	item, err := h.catalog.Create(request.PathValue("kind"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("catalog asset created", "kind", item.Kind, "asset_id", item.ID)
	writeJSON(response, http.StatusCreated, item)
}

// handleUpdateAsset changes mutable metadata, enabled state, skills, or selected version.
func (h *Handler) handleUpdateAsset(response http.ResponseWriter, request *http.Request) {
	var input catalog.UpsertInput
	if !decodeJSON(response, request, 512*1024, &input) {
		return
	}
	item, err := h.catalog.Update(request.PathValue("kind"), request.PathValue("id"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("catalog asset updated", "kind", item.Kind, "asset_id", item.ID, "revision", item.Revision)
	writeJSON(response, http.StatusOK, item)
}

// handleAddAssetVersion publishes immutable Markdown content.
func (h *Handler) handleAddAssetVersion(response http.ResponseWriter, request *http.Request) {
	var input catalog.VersionInput
	if !decodeJSON(response, request, 1024*1024, &input) {
		return
	}
	item, err := h.catalog.AddVersion(request.PathValue("kind"), request.PathValue("id"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, item)
}

// handleDeleteAsset removes a non-system asset with no task references.
func (h *Handler) handleDeleteAsset(response http.ResponseWriter, request *http.Request) {
	kind := request.PathValue("kind")
	id := request.PathValue("id")
	taskIDs, err := h.tasks.RelatedTaskIDs(request.Context(), kind, id)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if len(taskIDs) > 0 {
		writeError(response, http.StatusConflict, "ASSET_IN_USE", "资产已被任务引用，不能删除")
		return
	}
	if err := h.catalog.Delete(kind, id); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// handleAssetRelations reports task references and agent references for a skill.
func (h *Handler) handleAssetRelations(response http.ResponseWriter, request *http.Request) {
	kind := request.PathValue("kind")
	id := request.PathValue("id")
	taskIDs, err := h.tasks.RelatedTaskIDs(request.Context(), kind, id)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	agentIDs := make([]string, 0)
	if kind == catalog.KindSkill {
		agents, err := h.catalog.List(catalog.KindAgent)
		if err != nil {
			writeServiceError(response, err)
			return
		}
		for _, agent := range agents {
			if stringSliceContains(agent.DefaultSkillIDs, id) {
				agentIDs = append(agentIDs, agent.ID)
			}
		}
	}
	writeJSON(response, http.StatusOK, catalog.RelationSummary{AgentIDs: agentIDs, TaskIDs: taskIDs})
}

// handleListTasks scans all managed task directories.
func (h *Handler) handleListTasks(response http.ResponseWriter, request *http.Request) {
	items, err := h.tasks.List(request.Context())
	if err != nil {
		h.logger.Error("failed to list tasks", "error", err)
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// handleListTaskGroups returns durable navigation groups, including empty groups
// that no longer have a task directory beneath them.
func (h *Handler) handleListTaskGroups(response http.ResponseWriter, request *http.Request) {
	items, err := h.tasks.ListGroups(request.Context())
	if err != nil {
		h.logger.Error("failed to list task groups", "error", err)
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// handleGetTask returns metadata, plan, runtime state, and change history.
func (h *Handler) handleGetTask(response http.ResponseWriter, request *http.Request) {
	detail, err := h.tasks.Detail(request.Context(), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, detail)
}

// handleCreateTask creates the task directory that also acts as the project workspace.
func (h *Handler) handleCreateTask(response http.ResponseWriter, request *http.Request) {
	var input task.CreateInput
	if !decodeJSON(response, request, 64*1024, &input) {
		return
	}
	item, err := h.tasks.Create(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("task workspace created", "task_id", item.ID, "task_name", item.Name)
	writeJSON(response, http.StatusCreated, item)
}

// handleDeleteTask stops every task-owned runtime process before permanently
// removing the task workspace, including logs, uploads, source, and documents.
func (h *Handler) handleDeleteTask(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	if _, err := h.tasks.Get(request.Context(), taskID); err != nil {
		writeServiceError(response, err)
		return
	}
	if err := h.runtime.StopTask(taskID); err != nil {
		h.logger.Error("failed to stop task processes before deletion", "error", err, "task_id", taskID)
		writeServiceError(response, err)
		return
	}
	if err := h.tasks.StopDeployment(taskID); err != nil {
		h.logger.Error("failed to stop deployed project before deletion", "error", err, "task_id", taskID)
		writeServiceError(response, err)
		return
	}
	if err := h.tasks.Delete(request.Context(), taskID); err != nil {
		h.logger.Error("failed to delete task workspace", "error", err, "task_id", taskID)
		writeServiceError(response, err)
		return
	}
	h.logger.Info("task workspace and processes deleted", "task_id", taskID)
	response.WriteHeader(http.StatusNoContent)
}

// handleCloseTask stops active task processes and keeps the complete workspace in
// a terminal CLOSED state that can be inspected but cannot be changed or resumed.
func (h *Handler) handleCloseTask(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	if _, err := h.tasks.Get(request.Context(), taskID); err != nil {
		writeServiceError(response, err)
		return
	}
	if err := h.runtime.StopTask(taskID); err != nil {
		h.logger.Error("failed to stop task processes before closing", "error", err, "task_id", taskID)
		writeServiceError(response, err)
		return
	}
	item, err := h.tasks.Close(request.Context(), taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("task closed for read-only inspection", "task_id", taskID)
	writeJSON(response, http.StatusOK, item)
}

// handleGeneratePlan invokes the deterministic task-builder fallback using catalog roles.
func (h *Handler) handleGeneratePlan(response http.ResponseWriter, request *http.Request) {
	plan, err := h.tasks.GeneratePlan(request.Context(), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

// handleSavePlan persists user-edited team capabilities and workflow dependencies.
func (h *Handler) handleSavePlan(response http.ResponseWriter, request *http.Request) {
	var plan task.Plan
	if !decodeJSON(response, request, 1024*1024, &plan) {
		return
	}
	saved, err := h.tasks.SavePlan(request.Context(), request.PathValue("id"), plan)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, saved)
}

// handlePlanMutation applies one serialized member, capability, resource, or DAG
// operation for both the Web UI and task agents.
func (h *Handler) handlePlanMutation(response http.ResponseWriter, request *http.Request) {
	var input task.PlanMutation
	if !decodeJSON(response, request, 1024*1024, &input) {
		return
	}
	taskID := request.PathValue("id")
	saved, err := h.tasks.ApplyPlanMutation(request.Context(), taskID, input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	h.logger.Info("team plan mutation applied", "task_id", taskID, "action", input.Action, "revision", saved.Revision)
	writeJSON(response, http.StatusOK, saved)
}

// handleStartTask materializes task-local assets and starts the project-manager session.
func (h *Handler) handleStartTask(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	detail, err := h.tasks.Start(request.Context(), id)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	prompt, err := h.tasks.AgentPrompt(id, "project-manager", "任务已经开始。请读取 .cc-hardness/team.json 和 .cc-hardness/workflow/definition.json，汇总当前计划并准备协调团队。")
	if err != nil {
		writeServiceError(response, err)
		return
	}
	managerSession, err := h.runtime.StartOrSend(ccruntime.StartInput{TaskID: id, MemberID: "project-manager", Cwd: detail.RootPath, Prompt: prompt})
	if err != nil {
		_, _ = h.tasks.SetStatus(request.Context(), id, task.StatusPlanning)
		h.logger.Error("failed to start project manager session", "error", err, "task_id", id)
		writeError(response, http.StatusBadGateway, "CLAUDE_START_FAILED", err.Error())
		return
	}
	if _, err := h.tasks.SetActiveSession(request.Context(), id, managerSession.MemberID, managerSession.ID); err != nil {
		h.logger.Error("failed to persist project manager session", "error", err, "task_id", id, "session_id", managerSession.ID)
	}
	primarySession := managerSession
	if detail.Plan != nil && detail.Runtime != nil {
		for index := range detail.Plan.Workflow {
			node := &detail.Plan.Workflow[index]
			if node.Type != "AGENT" || detail.Runtime.NodeStates[node.Key] != "READY" {
				continue
			}
			entrySession, startErr := h.startAgentNode(request.Context(), id, detail, node)
			if startErr != nil {
				h.logger.Error("failed to start workflow entry node", "error", startErr, "task_id", id, "node_key", node.Key)
				continue
			}
			primarySession = entrySession
		}
	}
	detail, err = h.tasks.Detail(request.Context(), id)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"task": detail, "session": primarySession})
}

// handleAdvanceTask archives the user-approved phase and starts every newly ready
// agent task in the next phase. Dynamic workflow phases use the same transition.
func (h *Handler) handleAdvanceTask(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	before, beforeErr := h.tasks.Detail(request.Context(), taskID)
	if beforeErr == nil && before.Plan != nil {
		interrupted := map[string]bool{}
		for _, node := range before.Plan.Workflow {
			if node.Phase != before.Phase || node.AssigneeID == "" || interrupted[node.AssigneeID] {
				continue
			}
			if active, ok := h.runtime.ByMember(taskID, node.AssigneeID); ok && active.Status == "RUNNING" {
				if interruptErr := h.runtime.Interrupt(active.ID); interruptErr != nil {
					h.logger.Error("failed to interrupt archived phase session", "error", interruptErr, "task_id", taskID, "member_id", node.AssigneeID)
				}
			}
			interrupted[node.AssigneeID] = true
		}
	}
	detail, err := h.tasks.AdvanceStage(request.Context(), taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if detail.Plan != nil && detail.Runtime != nil {
		for index := range detail.Plan.Workflow {
			node := &detail.Plan.Workflow[index]
			// Explicit background preparation may cross an approval boundary only for
			// reversible artifacts. After architecture approval, normal development may
			// also run during prototype because its required business baseline is fixed.
			eligiblePhase := node.Background || node.Phase == detail.Phase || (detail.Phase == "PROTOTYPE" && node.Phase == "DEVELOPMENT")
			if !eligiblePhase || node.Type != "AGENT" {
				continue
			}
			nodeState := detail.Runtime.NodeStates[node.Key]
			orphanedRunning := nodeState == "RUNNING"
			if orphanedRunning {
				if active, exists := h.runtime.ByMember(taskID, node.AssigneeID); exists && active.Status == "RUNNING" {
					continue
				}
			}
			if nodeState != "READY" && !orphanedRunning {
				continue
			}
			if _, startErr := h.startAgentNode(request.Context(), taskID, detail, node); startErr != nil {
				h.logger.Error("failed to start next phase node", "error", startErr, "task_id", taskID, "node_key", node.Key)
			}
		}
	}
	latest, err := h.tasks.Detail(request.Context(), taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, latest)
}

// handlePauseTask pauses workflow progress and interrupts live sessions on user request.
func (h *Handler) handlePauseTask(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	item, err := h.tasks.SetStatus(request.Context(), taskID, task.StatusPaused)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if err := h.runtime.InterruptTask(taskID); err != nil {
		h.logger.Error("failed to interrupt all task sessions", "error", err, "task_id", taskID)
	}
	writeJSON(response, http.StatusOK, item)
}

// handleResumeTask resumes a paused task without recreating its plan.
func (h *Handler) handleResumeTask(response http.ResponseWriter, request *http.Request) {
	item, err := h.tasks.SetStatus(request.Context(), request.PathValue("id"), task.StatusRunning)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, item)
}

// handleAcceptTask records explicit final user acceptance.
func (h *Handler) handleAcceptTask(response http.ResponseWriter, request *http.Request) {
	item, err := h.tasks.Accept(request.Context(), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, item)
}

// handleSaveDelivery stores the final frontend and backend service addresses.
func (h *Handler) handleSaveDelivery(response http.ResponseWriter, request *http.Request) {
	var input task.DeliveryInfo
	if !decodeJSON(response, request, 64*1024, &input) {
		return
	}
	delivery, err := h.tasks.SaveDelivery(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, delivery)
}

// handleDeployTask starts or replaces the completed task's persisted frontend
// and backend deployment using explicit ports or OS-allocated random ports.
func (h *Handler) handleDeployTask(response http.ResponseWriter, request *http.Request) {
	var input task.DeployInput
	if !decodeJSON(response, request, 64*1024, &input) {
		return
	}
	taskID := request.PathValue("id")
	delivery, err := h.tasks.Deploy(request.Context(), taskID, input)
	if err != nil {
		h.logger.Error("failed to deploy completed task", "error", err, "task_id", taskID)
		writeServiceError(response, err)
		return
	}
	h.logger.Info("completed task deployed", "task_id", taskID, "frontend_port", delivery.FrontendPort, "backend_port", delivery.BackendPort)
	writeJSON(response, http.StatusOK, delivery)
}

// handleCreateChange appends a requirement or defect for project-manager routing.
func (h *Handler) handleCreateChange(response http.ResponseWriter, request *http.Request) {
	var input task.ChangeInput
	if !decodeJSON(response, request, 64*1024, &input) {
		return
	}
	change, err := h.tasks.AddChange(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	taskID := request.PathValue("id")
	item, taskErr := h.tasks.Get(request.Context(), taskID)
	if taskErr != nil {
		h.logger.Error("failed to inspect task before change dispatch", "error", taskErr, "task_id", taskID, "change_id", change.ID)
	}
	if taskErr == nil && taskAllowsRuntime(item.Status) {
		dispatchText := "收到新的补充需求或缺陷，请分析影响范围并分配给合适成员：\n\n" + change.Description
		if existing, ok := h.runtime.ByMember(taskID, "project-manager"); ok && existing.Status == "RUNNING" {
			if err := h.runtime.Send(existing.ID, dispatchText); err != nil {
				h.logger.Error("failed to dispatch change to project manager", "error", err, "task_id", taskID, "change_id", change.ID)
			}
		} else if root, rootErr := h.tasks.Root(taskID); rootErr == nil {
			if prompt, promptErr := h.tasks.AgentPrompt(taskID, "project-manager", dispatchText); promptErr == nil {
				if session, startErr := h.runtime.Start(ccruntime.StartInput{TaskID: taskID, MemberID: "project-manager", Cwd: root, Prompt: prompt}); startErr == nil {
					if _, persistErr := h.tasks.SetActiveSession(request.Context(), taskID, session.MemberID, session.ID); persistErr != nil {
						h.logger.Error("failed to persist change dispatch session", "error", persistErr, "task_id", taskID, "session_id", session.ID)
					}
				} else {
					h.logger.Error("failed to start project manager for change", "error", startErr, "task_id", taskID, "change_id", change.ID)
				}
			} else {
				h.logger.Error("failed to build project manager change prompt", "error", promptErr, "task_id", taskID, "change_id", change.ID)
			}
		} else {
			h.logger.Error("failed to resolve task root for change dispatch", "error", rootErr, "task_id", taskID, "change_id", change.ID)
		}
	}
	writeJSON(response, http.StatusCreated, change)
}

// handleNodeStatus records approval, completion, failure, or retry transitions.
func (h *Handler) handleNodeStatus(response http.ResponseWriter, request *http.Request) {
	var input task.NodeStatusInput
	if !decodeJSON(response, request, 32*1024, &input) {
		return
	}
	item, err := h.tasks.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if !taskAllowsRuntime(item.Status) {
		writeError(response, http.StatusConflict, "TASK_NOT_RUNNING", "任务未运行或已暂停，不能更新节点")
		return
	}
	state, err := h.tasks.SetNodeStatus(request.Context(), request.PathValue("id"), request.PathValue("nodeKey"), input.Status)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, state)
}

// handleRunNode starts or reuses the assigned member's Claude session for one DAG node.
func (h *Handler) handleRunNode(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	nodeKey := request.PathValue("nodeKey")
	detail, err := h.tasks.Detail(request.Context(), taskID)
	if err != nil || detail.Plan == nil {
		writeServiceError(response, errors.New("task plan is unavailable"))
		return
	}
	if !taskAllowsRuntime(detail.Status) {
		writeError(response, http.StatusConflict, "TASK_NOT_RUNNING", "任务未运行或已暂停，不能启动节点")
		return
	}
	var selected *task.WorkflowNode
	for index := range detail.Plan.Workflow {
		if detail.Plan.Workflow[index].Key == nodeKey {
			selected = &detail.Plan.Workflow[index]
			break
		}
	}
	if selected == nil {
		writeError(response, http.StatusNotFound, "NODE_NOT_FOUND", "工作流节点不存在")
		return
	}
	if selected.Type == "USER_APPROVAL" {
		state, err := h.tasks.SetNodeStatus(request.Context(), taskID, nodeKey, "WAITING_USER")
		if err != nil {
			writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"runtime": state})
		return
	}
	session, err := h.startAgentNode(request.Context(), taskID, detail, selected)
	if err != nil {
		writeError(response, http.StatusBadGateway, "CLAUDE_START_FAILED", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, session)
}

// startAgentNode transitions one ready or failed node, starts its member session,
// and persists the session route used by task switching and runtime recovery metadata.
func (h *Handler) startAgentNode(ctx context.Context, taskID string, detail task.TaskDetail, node *task.WorkflowNode) (ccruntime.SessionInfo, error) {
	workflowInstruction := workflowInstructionForNode(detail, node)
	prompt, err := h.tasks.AgentPrompt(taskID, node.AssigneeID, workflowInstruction)
	if err != nil {
		return ccruntime.SessionInfo{}, err
	}
	if _, err := h.tasks.SetNodeStatus(ctx, taskID, node.Key, "RUNNING"); err != nil {
		return ccruntime.SessionInfo{}, err
	}
	afterSequence := int64(0)
	if existing, ok := h.runtime.ByMember(taskID, node.AssigneeID); ok && existing.Status == "RUNNING" {
		if existingEvents, eventsErr := h.runtime.Events(existing.ID, 0); eventsErr == nil {
			for _, event := range existingEvents {
				if event.Sequence > afterSequence {
					afterSequence = event.Sequence
				}
			}
		}
	}
	session, err := h.runtime.StartOrSend(ccruntime.StartInput{TaskID: taskID, MemberID: node.AssigneeID, Cwd: detail.RootPath, Prompt: prompt})
	if err != nil {
		_, _ = h.tasks.SetNodeStatus(ctx, taskID, node.Key, "FAILED")
		return ccruntime.SessionInfo{}, err
	}
	if _, err := h.tasks.SetActiveSession(ctx, taskID, session.MemberID, session.ID); err != nil {
		h.logger.Error("failed to persist workflow session", "error", err, "task_id", taskID, "session_id", session.ID)
	}
	go h.monitorAgentNode(taskID, node.Key, session.ID, afterSequence)
	return session, nil
}

// workflowInstructionForNode gives the product and architecture checkpoints
// intentionally narrow responsibilities. Other dynamic roles keep the shared
// execution contract generated from their workflow node.
func workflowInstructionForNode(detail task.TaskDetail, node *task.WorkflowNode) string {
	prefix := "__CC_HARDNESS_WORKFLOW__ 系统已将当前阶段任务分派给你：" + node.Name + "\n\n"
	switch {
	case node.Phase == "ALIGNMENT" && node.AssigneeID == "product-manager":
		return prefix +
			"你本轮只负责提炼产品需求并完成 PRD，不进行架构设计、UI 设计、编码、测试或团队调整。\n" +
			"请先读取任务背景与已有材料，直接完成一版可评审 PRD；除非缺少会彻底改变产品方向的决定性信息，不要在写初稿前向用户提问。\n" +
			"PRD 必须包含项目详情、项目目的、目标用户、角色权限、完整功能模块及每个模块的功能点、主流程、异常流程、范围边界和可验证验收标准。\n" +
			"产物写入 docs/agents/product-manager/alignment，主文件使用“" + detail.Name + "+PRD文档.md”命名。\n" +
			"完成初稿后按 grill-me 技能只追问仍会显著改变范围、权限、核心流程或验收的决定：一次一个问题并附推荐答案；用户可说接受默认值直接收口。\n" +
			"完成后只说明生成或修改的 PRD 文件、概括项目目的与功能模块，并询问用户是否需要修改；不要继续执行其他阶段工作。"
	case node.Key == "architecture-contract" && node.AssigneeID == "architect":
		return prefix +
			"这是可回退的后台预设计，不等待用户完成 PRD 评审。只读取当前 PRD 初稿，输出领域边界、数据对象、API 路径、请求响应和错误模型草案。\n" +
			"产物写入 docs/agents/architect/architecture，主文件命名为“" + detail.Name + "+接口契约文档.md”。明确标注草案假设和待详细设计校正项。\n" +
			"不要编写详细业务流程，不要替用户做产品决策，不要实现代码；完成后保持会话存活，等待详细架构任务。"
	case node.Phase == "ARCHITECTURE" && node.AssigneeID == "architect":
		return prefix +
			"你本轮根据已经确认的 PRD 和接口契约草案输出详细技术设计，不扩写产品需求、不进行 UI 设计、业务编码、测试或团队调整。\n" +
			"请读取产品经理固定目录中的确认版 PRD，直接生成总体/概要设计、详细设计、数据设计、API 契约、部署与运行等实际需要的技术文档，写入 docs/agents/architect/architecture。\n" +
			"每个文件使用“" + detail.Name + "+文档类型.md”的格式命名，例如“" + detail.Name + "+概要设计文档.md”，并保持页面、接口、实体和权限一致。\n" +
			"完成后列出生成或修改的技术文档，只用几句话说明技术栈及关键边界，然后询问用户是否需要修改；不要继续执行下一阶段工作。"
	default:
		return prefix + node.Description +
			"\n请立即读取任务资料和上游产物并开始执行，不要让用户重复确认是否开始。只有缺少决定性业务信息时才提出少量问题。" +
			"\n完成后简洁说明生成或修改的文件、仍存在的阻塞，并询问用户是否还需要修改。"
	}
}

// monitorAgentNode follows one dispatched Claude turn until its terminal result.
// The Claude process remains persistent for future conversation turns, therefore
// node completion is derived from the result event rather than process exit.
func (h *Handler) monitorAgentNode(taskID string, nodeKey string, sessionID string, afterSequence int64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cursor := afterSequence
	for {
		events, err := h.runtime.Events(sessionID, cursor)
		if err != nil {
			h.logger.Error("failed to read workflow session events", "error", err, "task_id", taskID, "node_key", nodeKey, "session_id", sessionID)
			return
		}
		for _, event := range events {
			if event.Sequence > cursor {
				cursor = event.Sequence
			}
			status, finished := workflowTerminalStatus(event)
			if !finished {
				continue
			}
			if status == "" {
				h.logger.Info("workflow node monitor stopped after intentional session exit", "task_id", taskID, "node_key", nodeKey, "session_id", sessionID)
				return
			}
			h.finishAgentNode(taskID, nodeKey, sessionID, status)
			return
		}
		<-ticker.C
	}
}

// workflowTerminalStatus maps retained Claude lifecycle events to a workflow
// terminal state. Intentional process shutdown archives a conversation without
// changing a node that may already have been approved by the user.
func workflowTerminalStatus(event ccruntime.Event) (string, bool) {
	payload, _ := event.Payload.(map[string]any)
	if event.Type == "claude_event" && payload["type"] == "result" {
		if isError, _ := payload["is_error"].(bool); isError {
			return "FAILED", true
		}
		return "SUCCEEDED", true
	}
	if event.Type == "process_exit" {
		if intentional, _ := payload["intentional"].(bool); intentional {
			return "", true
		}
		return "FAILED", true
	}
	return "", false
}

// finishAgentNode persists the terminal node result and dispatches newly ready
// agent nodes that are allowed to run in the current user-visible phase.
func (h *Handler) finishAgentNode(taskID string, nodeKey string, sessionID string, status string) {
	if _, err := h.tasks.SetNodeStatus(context.Background(), taskID, nodeKey, status); err != nil {
		h.logger.Error("failed to persist workflow node result", "error", err, "task_id", taskID, "node_key", nodeKey, "session_id", sessionID, "status", status)
		return
	}
	h.logger.Info("workflow node completed", "task_id", taskID, "node_key", nodeKey, "session_id", sessionID, "status", status)
	if status == "SUCCEEDED" {
		h.startReadyAgentNodes(taskID)
	}
}

// startReadyAgentNodes starts current-phase successors, explicitly reversible
// background preparation, and development that has passed architecture approval
// while the user is still reviewing the visual prototype.
func (h *Handler) startReadyAgentNodes(taskID string) {
	detail, err := h.tasks.Detail(context.Background(), taskID)
	if err != nil {
		h.logger.Error("failed to inspect ready workflow nodes", "error", err, "task_id", taskID)
		return
	}
	if detail.Plan == nil || detail.Runtime == nil {
		return
	}
	for index := range detail.Plan.Workflow {
		node := &detail.Plan.Workflow[index]
		eligiblePhase := node.Background || node.Phase == detail.Phase || (detail.Phase == "PROTOTYPE" && node.Phase == "DEVELOPMENT")
		if !eligiblePhase || node.Type != "AGENT" {
			continue
		}
		nodeState := detail.Runtime.NodeStates[node.Key]
		orphanedRunning := nodeState == "RUNNING"
		if orphanedRunning {
			if active, exists := h.runtime.ByMember(taskID, node.AssigneeID); exists && active.Status == "RUNNING" {
				continue
			}
		}
		if nodeState != "READY" && !orphanedRunning {
			continue
		}
		if _, startErr := h.startAgentNode(context.Background(), taskID, detail, node); startErr != nil {
			h.logger.Error("failed to start unlocked workflow node", "error", startErr, "task_id", taskID, "node_key", node.Key)
		}
	}
}

// handleTaskMessage routes user text to the chosen team member's persistent session.
func (h *Handler) handleTaskMessage(response http.ResponseWriter, request *http.Request) {
	var input struct {
		MemberID string `json:"memberId"`
		Text     string `json:"text"`
		// Restart marks a fresh browser entry. A live idle process is reused; only
		// a missing or exited process requires a full context startup.
		Restart bool `json:"restart"`
	}
	if !decodeJSON(response, request, 128*1024, &input) {
		return
	}
	taskID := request.PathValue("id")
	item, err := h.tasks.Get(request.Context(), taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	builderConversation := strings.HasPrefix(input.MemberID, "task-builder")
	builderPlanning := builderConversation && item.Phase == task.PhasePlanning
	if !taskAllowsRuntime(item.Status) && !builderPlanning {
		writeError(response, http.StatusConflict, "TASK_NOT_RUNNING", "任务未运行或已暂停，不能发送成员消息")
		return
	}
	existing, hasExisting := h.runtime.ByMember(taskID, input.MemberID)
	if hasExisting && existing.Status == "RUNNING" {
		// Never discard a real user message merely because the browser entered a
		// fresh page context. Busy sessions reject the turn; idle sessions receive
		// it through the existing pipe without process restart or context replay.
		if existing.Busy {
			writeError(response, http.StatusConflict, "AGENT_BUSY", "智能体正在处理上一条任务，请等待完成后再发送")
			return
		}
		if sendErr := h.runtime.Send(existing.ID, input.Text); sendErr == nil {
			latest, infoErr := h.runtime.Info(existing.ID)
			if infoErr == nil {
				existing = latest
			}
			if !builderConversation {
				if _, err := h.tasks.SetActiveSession(request.Context(), taskID, existing.MemberID, existing.ID); err != nil {
					h.logger.Error("failed to persist reused member session", "error", err, "task_id", taskID, "session_id", existing.ID)
				}
			}
			existing.Reconnected = input.Restart
			if input.Restart {
				h.logger.Info("reused idle member session after workspace entry", "task_id", taskID, "member_id", input.MemberID, "session_id", existing.ID)
			}
			writeJSON(response, http.StatusOK, existing)
			return
		} else if !ccruntime.IsClosedPipeError(sendErr) {
			writeServiceError(response, sendErr)
			return
		}
		// The child can exit between the status lookup and stdin write. A closed
		// pipe is recovered below by creating a fresh process with the full prompt.
		h.logger.Info("stale member pipe detected; starting replacement session", "task_id", taskID, "member_id", input.MemberID, "session_id", existing.ID)
		_ = h.runtime.Stop(existing.ID)
	}
	root, err := h.tasks.Root(taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	prompt, err := h.tasks.AgentPrompt(taskID, input.MemberID, input.Text)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	session, err := h.runtime.Start(ccruntime.StartInput{TaskID: taskID, MemberID: input.MemberID, Cwd: root, Prompt: prompt})
	if err != nil {
		writeError(response, http.StatusBadGateway, "CLAUDE_MESSAGE_FAILED", err.Error())
		return
	}
	if !builderConversation {
		if _, err := h.tasks.SetActiveSession(request.Context(), taskID, session.MemberID, session.ID); err != nil {
			h.logger.Error("failed to persist member session", "error", err, "task_id", taskID, "session_id", session.ID)
		}
	}
	writeJSON(response, http.StatusOK, session)
}

// handleListTaskFiles returns a bounded artifact tree without exposing platform
// files outside the task. Generated frontend and backend projects are represented
// by their root folders so dependency and source trees cannot flood the document UI.
func (h *Handler) handleListTaskFiles(response http.ResponseWriter, request *http.Request) {
	root, err := h.tasks.Root(request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	items := make([]map[string]any, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		normalizedRelative := filepath.ToSlash(relative)
		firstSegment := strings.Split(normalizedRelative, "/")[0]
		if entry.IsDir() && (firstSegment == "frontend" || firstSegment == "backend") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			items = append(items, map[string]any{"path": normalizedRelative, "directory": true, "size": info.Size(), "updatedAt": info.ModTime()})
			return filepath.SkipDir
		}
		// Runtime controls, dependency caches, and source-control internals are not
		// stage artifacts and can otherwise exhaust the bounded result before docs.
		if entry.IsDir() && stringSliceContains([]string{".cc-hardness", ".claude", ".git", "node_modules"}, filepath.Base(path)) {
			return filepath.SkipDir
		}
		if len(items) >= 500 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		items = append(items, map[string]any{"path": normalizedRelative, "directory": entry.IsDir(), "size": info.Size(), "updatedAt": info.ModTime()})
		return nil
	})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// handleUploadTaskFile saves user-provided planning material below inputs/.
func (h *Handler) handleUploadTaskFile(response http.ResponseWriter, request *http.Request) {
	item, err := h.tasks.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if item.Status == task.StatusClosed {
		writeError(response, http.StatusConflict, "TASK_CLOSED", "任务已关闭，仅允许查看")
		return
	}
	root, err := h.tasks.Root(request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 32*1024*1024)
	if err := request.ParseMultipartForm(32 * 1024 * 1024); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_UPLOAD", "上传文件无效或超过 32MB")
		return
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		writeError(response, http.StatusBadRequest, "FILE_REQUIRED", "请选择文件")
		return
	}
	defer file.Close()
	name := filepath.Base(header.Filename)
	if name == "." || name == "" {
		writeError(response, http.StatusBadRequest, "INVALID_FILENAME", "文件名不合法")
		return
	}
	targetDirectory := filepath.Join(root, "inputs")
	if err := os.MkdirAll(targetDirectory, 0o755); err != nil {
		writeServiceError(response, err)
		return
	}
	target, err := os.OpenFile(filepath.Join(targetDirectory, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	_, copyErr := io.Copy(target, file)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		writeServiceError(response, errors.New("failed to save uploaded file"))
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"path": filepath.ToSlash(filepath.Join("inputs", name))})
}

// handleOpenTaskFile validates a task-relative file path and asks the host file
// manager to reveal it. Validation happens before process launch so the endpoint
// cannot be used to browse outside the selected task workspace.
func (h *Handler) handleOpenTaskFile(response http.ResponseWriter, request *http.Request) {
	var input struct {
		// Path is the slash-normalized task-relative path returned by handleListTaskFiles.
		Path string `json:"path"`
	}
	if !decodeJSON(response, request, 32*1024, &input) {
		return
	}
	root, err := h.tasks.Root(request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	relative := filepath.Clean(filepath.FromSlash(strings.TrimSpace(input.Path)))
	if relative == "." || filepath.IsAbs(relative) {
		writeError(response, http.StatusBadRequest, "INVALID_FILE_PATH", "文件路径必须位于当前任务目录内")
		return
	}
	target := filepath.Join(root, relative)
	resolvedRoot, rootErr := filepath.Abs(root)
	resolvedTarget, targetErr := filepath.Abs(target)
	if rootErr != nil || targetErr != nil {
		writeServiceError(response, errors.New("failed to resolve task file path"))
		return
	}
	boundary, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || boundary == ".." || strings.HasPrefix(boundary, ".."+string(filepath.Separator)) {
		writeError(response, http.StatusBadRequest, "INVALID_FILE_PATH", "文件路径必须位于当前任务目录内")
		return
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil {
		writeError(response, http.StatusNotFound, "TASK_FILE_NOT_FOUND", "原文件或文件夹不存在")
		return
	}
	actualRoot, rootLinkErr := filepath.EvalSymlinks(resolvedRoot)
	actualTarget, targetLinkErr := filepath.EvalSymlinks(resolvedTarget)
	if rootLinkErr != nil || targetLinkErr != nil {
		writeError(response, http.StatusNotFound, "TASK_FILE_NOT_FOUND", "无法解析原文件位置")
		return
	}
	actualBoundary, err := filepath.Rel(actualRoot, actualTarget)
	if err != nil || actualBoundary == ".." || strings.HasPrefix(actualBoundary, ".."+string(filepath.Separator)) {
		writeError(response, http.StatusBadRequest, "INVALID_FILE_PATH", "符号链接指向了当前任务目录之外")
		return
	}
	resolvedTarget = actualTarget

	var command *exec.Cmd
	switch gort.GOOS {
	case "windows":
		if info.IsDir() {
			command = exec.Command("explorer.exe", resolvedTarget)
		} else {
			command = exec.Command("explorer.exe", "/select,"+resolvedTarget)
		}
	case "darwin":
		if info.IsDir() {
			command = exec.Command("open", resolvedTarget)
		} else {
			command = exec.Command("open", "-R", resolvedTarget)
		}
	default:
		if info.IsDir() {
			command = exec.Command("xdg-open", resolvedTarget)
		} else {
			command = exec.Command("xdg-open", filepath.Dir(resolvedTarget))
		}
	}
	if err := command.Start(); err != nil {
		h.logger.Error("failed to reveal task file", "error", err, "task_id", request.PathValue("id"), "path", boundary)
		writeError(response, http.StatusInternalServerError, "OPEN_FILE_FAILED", "无法打开原文件位置")
		return
	}
	// The file manager outlives this request; releasing the process handle avoids
	// retaining OS resources while intentionally not waiting for its UI to close.
	if err := command.Process.Release(); err != nil {
		h.logger.Error("failed to release file manager process", "error", err, "task_id", request.PathValue("id"), "path", boundary)
	}
	h.logger.Info("revealed task artifact", "task_id", request.PathValue("id"), "path", boundary, "directory", info.IsDir())
	response.WriteHeader(http.StatusNoContent)
}

// handleListTaskSessions merges live runtime metadata with archived task-local
// session logs so diagnostics survive a backend restart.
func (h *Handler) handleListTaskSessions(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	detail, err := h.tasks.Detail(request.Context(), taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	items := make([]ccruntime.SessionInfo, 0)
	seen := map[string]bool{}
	for _, session := range h.runtime.List() {
		if session.TaskID != taskID {
			continue
		}
		items = append(items, session)
		seen[session.ID] = true
	}
	logRoot := filepath.Join(detail.RootPath, ".cc-hardness", "logs")
	manifests, err := filepath.Glob(filepath.Join(logRoot, "*.stdin.ndjson"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	for _, manifest := range manifests {
		sessionID := strings.TrimSuffix(filepath.Base(manifest), ".stdin.ndjson")
		if seen[sessionID] {
			continue
		}
		info, statErr := os.Stat(manifest)
		if statErr != nil {
			continue
		}
		items = append(items, ccruntime.SessionInfo{
			ID: sessionID, TaskID: taskID, MemberID: inferArchivedMemberID(manifest, detail.Plan),
			Status: "ARCHIVED", StartedAt: info.ModTime(),
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// handleTaskSessionEvents returns live in-memory events when available and falls
// back to persisted stdin/stdout/stderr logs for sessions from an earlier process.
func (h *Handler) handleTaskSessionEvents(response http.ResponseWriter, request *http.Request) {
	taskID := request.PathValue("id")
	sessionID := request.PathValue("sessionId")
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	if !validSessionID(sessionID) {
		writeError(response, http.StatusBadRequest, "INVALID_SESSION_ID", "会话标识不合法")
		return
	}
	if info, err := h.runtime.Info(sessionID); err == nil && info.TaskID == taskID {
		events, eventErr := h.runtime.Events(sessionID, after)
		if eventErr != nil {
			writeServiceError(response, eventErr)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": events})
		return
	}
	root, err := h.tasks.Root(taskID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	events, err := readArchivedSessionEvents(filepath.Join(root, ".cc-hardness", "logs"), sessionID, after)
	if err != nil {
		writeError(response, http.StatusNotFound, "SESSION_LOG_NOT_FOUND", "会话日志不存在")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": events})
}

// handleListSessions returns runtime metadata for process monitoring.
func (h *Handler) handleListSessions(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{"items": h.runtime.List()})
}

// handleGetSession returns one reconnectable session route used by member switching.
func (h *Handler) handleGetSession(response http.ResponseWriter, request *http.Request) {
	info, err := h.runtime.Info(request.PathValue("id"))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, info)
}

// handleSessionEvents returns ordered events after an optional sequence query.
func (h *Handler) handleSessionEvents(response http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	events, err := h.runtime.Events(request.PathValue("id"), after)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": events})
}

// handleSessionMessage sends raw follow-up text to an already selected process.
func (h *Handler) handleSessionMessage(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Text string `json:"text"`
	}
	if !decodeJSON(response, request, 128*1024, &input) {
		return
	}
	if err := h.runtime.Send(request.PathValue("id"), input.Text); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "sent"})
}

// handlePermissionDecision approves or denies one Claude Code tool request.
func (h *Handler) handlePermissionDecision(response http.ResponseWriter, request *http.Request) {
	var input struct {
		RequestID    string         `json:"requestId"`
		Allow        bool           `json:"allow"`
		UpdatedInput map[string]any `json:"updatedInput"`
		Message      string         `json:"message"`
	}
	if !decodeJSON(response, request, 256*1024, &input) {
		return
	}
	if err := h.runtime.RespondPermission(request.PathValue("id"), input.RequestID, input.Allow, input.UpdatedInput, input.Message); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "decided"})
}

// handleInterruptSession sends a graceful interrupt control request.
func (h *Handler) handleInterruptSession(response http.ResponseWriter, request *http.Request) {
	if err := h.runtime.Interrupt(request.PathValue("id")); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "interrupting"})
}

// handleStopSession terminates one local Claude Code process.
func (h *Handler) handleStopSession(response http.ResponseWriter, request *http.Request) {
	if err := h.runtime.Stop(request.PathValue("id")); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// serveWeb serves compiled static files and falls back to index.html for client routes.
func (h *Handler) serveWeb(response http.ResponseWriter, request *http.Request) {
	indexPath := filepath.Join(h.webRoot, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		writeError(response, http.StatusServiceUnavailable, "WEB_NOT_BUILT", "Web 前端尚未构建，请运行一键启动脚本")
		return
	}
	requestedPath := filepath.Clean(strings.TrimPrefix(request.URL.Path, "/"))
	assetPath := filepath.Join(h.webRoot, requestedPath)
	if requestedPath != "." {
		relativePath, relativeErr := filepath.Rel(h.webRoot, assetPath)
		if relativeErr != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			writeError(response, http.StatusBadRequest, "INVALID_ASSET_PATH", "静态资源路径不合法")
			return
		}
		if info, err := os.Stat(assetPath); err == nil && !info.IsDir() {
			if contentType := mime.TypeByExtension(filepath.Ext(assetPath)); contentType != "" {
				response.Header().Set("Content-Type", contentType)
			}
			http.ServeFile(response, request, assetPath)
			return
		}
	}
	http.ServeFile(response, request, indexPath)
}

// decodeJSON reads a bounded request body and rejects unknown fields.
func decodeJSON(response http.ResponseWriter, request *http.Request, limit int64, target any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := "请求内容不是有效 JSON：" + err.Error()
		if errors.Is(err, io.EOF) {
			message = "请求内容不能为空"
		}
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", message)
		return false
	}
	return true
}

// writeServiceError maps expected file-domain failures to useful HTTP responses.
func writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrNotFound), errors.Is(err, catalog.ErrNotFound):
		writeError(response, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, task.ErrInvalidID):
		writeError(response, http.StatusBadRequest, "INVALID_ID", err.Error())
	case errors.Is(err, catalog.ErrConflict):
		writeError(response, http.StatusConflict, "CONFLICT", err.Error())
	default:
		writeError(response, http.StatusBadRequest, "REQUEST_FAILED", err.Error())
	}
}

// writeJSON serializes a response with a stable JSON content type.
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

// writeError returns the shared machine-readable API error envelope.
func writeError(response http.ResponseWriter, status int, code string, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// commandExists reports whether an executable can be found through PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// stringSliceContains reports whether values contains target.
func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// validSessionID accepts only generated session route characters before joining
// the value into the task-local logs directory.
func validSessionID(value string) bool {
	if !strings.HasPrefix(value, "ses_") || filepath.Base(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// inferArchivedMemberID extracts the role name embedded in the first persisted
// prompt and maps it back to a stable team member route when possible.
func inferArchivedMemberID(stdinPath string, plan *task.Plan) string {
	file, err := os.Open(stdinPath)
	if err != nil {
		return "unknown-agent"
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if !scanner.Scan() {
		return "unknown-agent"
	}
	var envelope struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
		return "unknown-agent"
	}
	content := envelope.Message.Content
	startMarker := "你在 cc-hardness 团队中以 "
	start := strings.Index(content, startMarker)
	if start < 0 {
		return "unknown-agent"
	}
	remainder := content[start+len(startMarker):]
	end := strings.Index(remainder, " 身份工作")
	if end < 0 {
		return "unknown-agent"
	}
	name := strings.TrimSpace(remainder[:end])
	if name == "任务构建智能体" || name == "团队组建智能体" {
		return "task-builder-archived"
	}
	if plan != nil {
		for _, member := range plan.Team {
			if member.Name == name {
				return member.ID
			}
		}
	}
	knownMembers := map[string]string{
		"项目经理": "project-manager", "产品经理": "product-manager", "架构师": "architect",
		"UI 设计师": "ui-designer", "前端工程师": "frontend-engineer", "后端工程师": "backend-engineer",
		"测试工程师": "test-engineer", "集成与交付工程师": "auditor",
	}
	if memberID := knownMembers[name]; memberID != "" {
		return memberID
	}
	return name
}

// readArchivedSessionEvents reconstructs a bounded diagnostic stream from the
// durable input, Claude output, and stderr logs of one archived session.
func readArchivedSessionEvents(logRoot string, sessionID string, after int64) ([]ccruntime.Event, error) {
	unifiedPath := filepath.Join(logRoot, sessionID+".events.ndjson")
	if unified, err := os.Open(unifiedPath); err == nil {
		defer unified.Close()
		events := make([]ccruntime.Event, 0, 256)
		scanner := bufio.NewScanner(unified)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			var event ccruntime.Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Sequence > after && runtimeEventUsefulToClient(event) {
				events = append(events, event)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		// stdout、stderr 和用户输入可能由不同 goroutine 近乎同时落盘。
		// 统一日志以分配时的 Sequence 为真实顺序，读取归档时显式恢复该顺序。
		sort.SliceStable(events, func(i, j int) bool {
			return events[i].Sequence < events[j].Sequence
		})
		return events, nil
	}

	// Legacy tasks predate the unified event stream. Their split logs are rebuilt
	// best-effort so old diagnostic history remains available after this upgrade.
	type logSource struct {
		suffix    string
		eventType string
		parseJSON bool
	}
	sources := []logSource{
		{suffix: ".stdin.ndjson", eventType: "user_input", parseJSON: true},
		{suffix: ".ndjson", eventType: "claude_event", parseJSON: true},
		{suffix: ".stderr.log", eventType: "stderr", parseJSON: false},
	}
	events := make([]ccruntime.Event, 0, 256)
	var sequence int64
	found := false
	for _, source := range sources {
		path := filepath.Join(logRoot, sessionID+source.suffix)
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		found = true
		info, _ := file.Stat()
		createdAt := time.Time{}
		if info != nil {
			createdAt = info.ModTime()
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			sequence++
			event := ccruntime.Event{Sequence: sequence, Type: source.eventType, CreatedAt: createdAt}
			if source.parseJSON {
				var payload any
				if err := json.Unmarshal(scanner.Bytes(), &payload); err != nil {
					event.Line = scanner.Text()
				} else {
					event.Payload = payload
				}
			} else {
				event.Line = scanner.Text()
			}
			events = append(events, event)
			if len(events) > 2000 {
				events = append([]ccruntime.Event(nil), events[len(events)-1500:]...)
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	if !found {
		return nil, os.ErrNotExist
	}
	filtered := make([]ccruntime.Event, 0, len(events))
	for _, event := range events {
		if event.Sequence > after && runtimeEventUsefulToClient(event) {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

// runtimeEventUsefulToClient excludes raw protocol deltas after the runtime has
// converted visible text into compact assistant_delta events. Complete messages,
// tools, lifecycle events, and compact live text remain available to the browser.
func runtimeEventUsefulToClient(event ccruntime.Event) bool {
	if runtimeEventIsClosedPipeNoise(event) {
		return false
	}
	if event.Type != "claude_event" {
		return true
	}
	payload, ok := event.Payload.(map[string]any)
	return !ok || payload["type"] != "stream_event"
}

// runtimeEventIsClosedPipeNoise hides legacy Windows shutdown diagnostics that
// are expected after an intentional process archive and offer no user action.
func runtimeEventIsClosedPipeNoise(event ccruntime.Event) bool {
	if event.Type != "error" && event.Type != "stderr" {
		return false
	}
	message := strings.ToLower(event.Line)
	if payload, ok := event.Payload.(map[string]any); ok {
		if value, valueOK := payload["message"].(string); valueOK {
			message += " " + strings.ToLower(value)
		}
	}
	return strings.Contains(message, "file already closed") ||
		strings.Contains(message, "pipe has been ended") ||
		strings.Contains(message, "read |0")
}

// taskAllowsRuntime reports whether task-level lifecycle permits Claude execution.
func taskAllowsRuntime(status string) bool {
	return status == task.StatusPlanning || status == task.StatusRunning || status == task.StatusAcceptance
}
