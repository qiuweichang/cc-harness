// Package runtime manages persistent Claude Code stream-json child processes.
package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	gort "runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event is one ordered runtime event returned to polling Web clients.
type Event struct {
	// Sequence is monotonic within one session.
	Sequence int64 `json:"sequence"`
	// Type is session_started, claude_event, permission_request, stderr, process_exit, or error.
	Type string `json:"type"`
	// Payload preserves a parsed Claude JSON event or platform metadata.
	Payload any `json:"payload,omitempty"`
	// Line carries stderr diagnostic text.
	Line string `json:"line,omitempty"`
	// CreatedAt is the UTC event time.
	CreatedAt time.Time `json:"createdAt"`
}

// SessionInfo is the safe session metadata exposed to the API.
type SessionInfo struct {
	ID       string `json:"id"`
	TaskID   string `json:"taskId"`
	MemberID string `json:"memberId"`
	PID      int    `json:"pid"`
	Status   string `json:"status"`
	// Busy reports whether Claude is still processing the most recently submitted
	// turn. Re-entry uses it to reconnect instead of killing active workflow work.
	Busy bool `json:"busy"`
	// Reconnected is response-only metadata set when page entry attaches to an
	// existing busy turn rather than creating a context-recovery turn.
	Reconnected bool      `json:"reconnected,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
}

// StartInput binds a new Claude Code process to one task member and workspace.
type StartInput struct {
	TaskID   string
	MemberID string
	Cwd      string
	Prompt   string
	Model    string
}

// Session owns one child process, its stdin serialization, and bounded event history.
type Session struct {
	// info is updated when the process exits and copied for API responses.
	info SessionInfo
	// cmd is the active Claude Code child process.
	cmd *exec.Cmd
	// cancel stops the process context during interrupt or shutdown.
	cancel context.CancelFunc
	// stdin receives complete NDJSON protocol messages.
	stdin io.WriteCloser
	// stdoutDone and stderrDone close after the corresponding pipe readers reach
	// EOF. Wait must not close command pipes before both readers finish.
	stdoutDone chan struct{}
	stderrDone chan struct{}
	// writeMu prevents user messages and control responses from interleaving.
	writeMu sync.Mutex
	// eventMu protects sequence and the bounded in-memory event history.
	eventMu sync.RWMutex
	// events retains recent structured events for browser polling and reconnection.
	events []Event
	// sequence is the latest emitted event sequence.
	sequence int64
	// stopRequested distinguishes intentional archive or shutdown from an
	// unexpected process exit. It is protected by eventMu with session info.
	stopRequested bool
	// logRoot is the task-local .cc-hardness/logs directory.
	logRoot string
	// logger records important process lifecycle events.
	logger *slog.Logger
}

// Manager indexes active and recently exited sessions by ID and task member.
type Manager struct {
	// sessions contains process handles and recent event history.
	sessions map[string]*Session
	// memberSessions routes chat to the most recent session for a task member.
	memberSessions map[string]string
	// mu protects both indexes across HTTP and process-exit goroutines.
	mu sync.RWMutex
	// logger is shared with every session.
	logger *slog.Logger
}

// NewManager creates an empty Claude Code process manager.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{sessions: map[string]*Session{}, memberSessions: map[string]string{}, logger: logger}
}

// Start launches a persistent stream-json Claude Code process and sends the initial prompt.
func (m *Manager) Start(input StartInput) (SessionInfo, error) {
	abs, err := filepath.Abs(input.Cwd)
	if err != nil {
		return SessionInfo{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return SessionInfo{}, errors.New("session working directory is unavailable")
	}
	id, err := newSessionID()
	if err != nil {
		return SessionInfo{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--replay-user-messages",
		"--permission-mode", "bypassPermissions",
	}
	if strings.TrimSpace(input.Model) != "" {
		args = append(args, "--model", input.Model)
	}
	command := buildCommand(ctx, args)
	command.Dir = abs
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return SessionInfo{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return SessionInfo{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return SessionInfo{}, err
	}

	session := &Session{
		info: SessionInfo{ID: id, TaskID: input.TaskID, MemberID: input.MemberID, Status: "STARTING", StartedAt: time.Now().UTC()},
		cmd:  command, cancel: cancel, stdin: stdin, events: make([]Event, 0, 256),
		stdoutDone: make(chan struct{}), stderrDone: make(chan struct{}),
		logRoot: filepath.Join(abs, ".cc-hardness", "logs"), logger: m.logger,
	}
	if err := os.MkdirAll(session.logRoot, 0o755); err != nil {
		cancel()
		return SessionInfo{}, err
	}
	if err := command.Start(); err != nil {
		cancel()
		return SessionInfo{}, fmt.Errorf("start claude: %w", err)
	}
	session.info.PID = command.Process.Pid
	session.info.Status = "RUNNING"
	m.mu.Lock()
	m.sessions[id] = session
	m.memberSessions[memberKey(input.TaskID, input.MemberID)] = id
	m.mu.Unlock()
	session.emit("session_started", map[string]any{"pid": session.info.PID, "cwd": abs}, "")
	m.logger.Info("claude session started", "session_id", id, "task_id", input.TaskID, "member_id", input.MemberID, "pid", session.info.PID)
	go session.readStdout(stdout)
	go session.readStderr(stderr)
	go session.wait()
	if strings.TrimSpace(input.Prompt) != "" {
		if err := session.SendUserMessage(input.Prompt); err != nil {
			session.cancel()
			return SessionInfo{}, err
		}
	}
	return session.Info(), nil
}

// StartOrSend reuses a running member session or starts a new one before sending text.
func (m *Manager) StartOrSend(input StartInput) (SessionInfo, error) {
	if existing, ok := m.ByMember(input.TaskID, input.MemberID); ok && existing.Status == "RUNNING" {
		if err := m.Send(existing.ID, input.Prompt); err != nil {
			return SessionInfo{}, err
		}
		return m.Info(existing.ID)
	}
	return m.Start(input)
}

// ByMember returns the latest session metadata for one task member.
func (m *Manager) ByMember(taskID string, memberID string) (SessionInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.memberSessions[memberKey(taskID, memberID)]
	if !ok {
		return SessionInfo{}, false
	}
	session := m.sessions[id]
	if session == nil {
		return SessionInfo{}, false
	}
	return session.Info(), true
}

// Send writes a normal user message to an active session.
func (m *Manager) Send(sessionID string, text string) error {
	session, err := m.get(sessionID)
	if err != nil {
		return err
	}
	return session.SendUserMessage(text)
}

// RespondPermission writes a can_use_tool decision back through the control protocol.
func (m *Manager) RespondPermission(sessionID string, requestID string, allow bool, updatedInput map[string]any, message string) error {
	session, err := m.get(sessionID)
	if err != nil {
		return err
	}
	response := map[string]any{"behavior": "deny", "message": message}
	if allow {
		response = map[string]any{"behavior": "allow", "updatedInput": updatedInput}
	}
	return session.writeJSON(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": response},
	})
}

// Interrupt requests a graceful runtime interrupt without immediately killing the process tree.
func (m *Manager) Interrupt(sessionID string) error {
	session, err := m.get(sessionID)
	if err != nil {
		return err
	}
	requestID, err := newSessionID()
	if err != nil {
		return err
	}
	return session.writeJSON(map[string]any{
		"type": "control_request", "request_id": requestID,
		"request": map[string]any{"subtype": "interrupt"},
	})
}

// Stop cancels the child process context and lets wait perform final cleanup.
func (m *Manager) Stop(sessionID string) error {
	session, err := m.get(sessionID)
	if err != nil {
		return err
	}
	session.eventMu.Lock()
	session.stopRequested = true
	pid := session.info.PID
	session.eventMu.Unlock()
	if gort.GOOS == "windows" && pid > 0 {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	session.cancel()
	return nil
}

// InterruptTask asks every running session owned by one task to stop its current turn.
// The processes remain alive so resuming the task can continue the same conversations.
func (m *Manager) InterruptTask(taskID string) error {
	m.mu.RLock()
	sessionIDs := make([]string, 0)
	for id, session := range m.sessions {
		info := session.Info()
		if info.TaskID == taskID && info.Status == "RUNNING" {
			sessionIDs = append(sessionIDs, id)
		}
	}
	m.mu.RUnlock()

	errorsBySession := make([]error, 0)
	for _, sessionID := range sessionIDs {
		if err := m.Interrupt(sessionID); err != nil {
			errorsBySession = append(errorsBySession, fmt.Errorf("interrupt session %s: %w", sessionID, err))
		}
	}
	return errors.Join(errorsBySession...)
}

// StopTask terminates every running Claude process owned by one task. The
// session list is copied before stopping so process cleanup never holds the
// manager index lock and archived diagnostics remain readable until deletion.
func (m *Manager) StopTask(taskID string) error {
	m.mu.RLock()
	sessionIDs := make([]string, 0)
	for id, session := range m.sessions {
		info := session.Info()
		if info.TaskID == taskID && (info.Status == "RUNNING" || info.Status == "STARTING") {
			sessionIDs = append(sessionIDs, id)
		}
	}
	m.mu.RUnlock()
	errorsBySession := make([]error, 0)
	for _, sessionID := range sessionIDs {
		if err := m.Stop(sessionID); err != nil {
			errorsBySession = append(errorsBySession, fmt.Errorf("stop session %s: %w", sessionID, err))
		}
	}
	return errors.Join(errorsBySession...)
}

// StopAll terminates every running Claude process during control-plane shutdown.
// A copied session list avoids holding the manager lock while operating on processes.
func (m *Manager) StopAll() error {
	m.mu.RLock()
	sessionIDs := make([]string, 0, len(m.sessions))
	for id, session := range m.sessions {
		if session.Info().Status == "RUNNING" {
			sessionIDs = append(sessionIDs, id)
		}
	}
	m.mu.RUnlock()
	errorsBySession := make([]error, 0)
	for _, sessionID := range sessionIDs {
		if err := m.Stop(sessionID); err != nil {
			errorsBySession = append(errorsBySession, fmt.Errorf("stop session %s: %w", sessionID, err))
		}
	}
	return errors.Join(errorsBySession...)
}

// Events returns recent session events with sequence greater than after.
func (m *Manager) Events(sessionID string, after int64) ([]Event, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	return session.Events(after), nil
}

// Info returns public metadata for one session without exposing process handles.
func (m *Manager) Info(sessionID string) (SessionInfo, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	return session.Info(), nil
}

// List returns metadata for all active and recently exited sessions.
func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]SessionInfo, 0, len(m.sessions))
	for _, session := range m.sessions {
		items = append(items, session.Info())
	}
	return items
}

// get returns a process session by route ID.
func (m *Manager) get(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session := m.sessions[sessionID]
	if session == nil {
		return nil, errors.New("session not found")
	}
	return session, nil
}

// Info returns a race-safe copy of session metadata.
func (s *Session) Info() SessionInfo {
	s.eventMu.RLock()
	defer s.eventMu.RUnlock()
	return s.info
}

// Events returns a copy of the bounded history after the requested sequence.
func (s *Session) Events(after int64) []Event {
	s.eventMu.RLock()
	defer s.eventMu.RUnlock()
	result := make([]Event, 0)
	for _, event := range s.events {
		if event.Sequence > after {
			result = append(result, event)
		}
	}
	return result
}

// SendUserMessage wraps text in the Claude stream-json user envelope.
func (s *Session) SendUserMessage(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("message text is required")
	}
	s.eventMu.Lock()
	s.info.Busy = true
	s.eventMu.Unlock()
	if err := s.writeJSON(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	}); err != nil {
		s.eventMu.Lock()
		s.info.Busy = false
		s.eventMu.Unlock()
		return err
	}
	// Mirror durable stdin into the ordered event stream so a live conversation
	// and its archived reconstruction expose the same user-message history.
	s.emit("user_input", map[string]any{"text": text}, "")
	return nil
}

// writeJSON serializes one complete NDJSON line under a session write mutex.
func (s *Session) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := appendLine(filepath.Join(s.logRoot, s.info.ID+".stdin.ndjson"), string(data)); err != nil {
		s.logger.Error("failed to persist Claude input", "error", err, "session_id", s.info.ID)
	}
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

// readStdout parses Claude NDJSON without allowing disk or browser consumers to block the pipe.
func (s *Session) readStdout(reader io.Reader) {
	defer close(s.stdoutDone)
	buffered := bufio.NewReaderSize(reader, 1024*1024)
	for {
		line, err := buffered.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			_ = appendLine(filepath.Join(s.logRoot, s.info.ID+".ndjson"), trimmed)
			var payload map[string]any
			if jsonErr := json.Unmarshal([]byte(trimmed), &payload); jsonErr != nil {
				s.emit("error", map[string]any{"message": "invalid Claude JSON", "line": trimmed}, "")
			} else if payload["type"] == "control_request" {
				s.handleControlRequest(payload)
			} else {
				// Preserve only visible text deltas as compact live events. Raw
				// stream envelopes still go to the durable protocol log, while
				// the browser receives a bounded event suitable for incremental UI.
				if text := assistantTextDelta(payload); text != "" {
					s.emit("assistant_delta", map[string]any{"text": text}, "")
				}
				if payload["type"] == "result" {
					s.eventMu.Lock()
					s.info.Busy = false
					s.eventMu.Unlock()
				}
				s.emit("claude_event", payload, "")
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosedPipeError(err) {
				s.emit("error", map[string]any{"message": err.Error()}, "")
			}
			return
		}
	}
}

// assistantTextDelta extracts user-visible text from one Claude stream envelope.
// Thinking and tool deltas stay in backend diagnostics and never enter the main chat.
func assistantTextDelta(payload map[string]any) string {
	if payload["type"] != "stream_event" {
		return ""
	}
	event, _ := payload["event"].(map[string]any)
	if event["type"] != "content_block_delta" {
		return ""
	}
	delta, _ := event["delta"].(map[string]any)
	if delta["type"] != "text_delta" {
		return ""
	}
	text, _ := delta["text"].(string)
	return text
}

// handleControlRequest automatically approves any defensive permission callback
// that still arrives in bypass mode. Task agents are intentionally unattended and
// must retain full edit capability without blocking on a hidden user prompt.
func (s *Session) handleControlRequest(payload map[string]any) {
	requestID, _ := payload["request_id"].(string)
	request, _ := payload["request"].(map[string]any)
	subtype, _ := request["subtype"].(string)
	if subtype == "can_use_tool" {
		response := map[string]any{"behavior": "allow"}
		if updatedInput, exists := request["input"]; exists {
			response["updatedInput"] = updatedInput
		}
		if err := s.writeJSON(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": response}}); err != nil {
			s.emit("error", map[string]any{"message": err.Error()}, "")
			return
		}
		s.emit("permission_auto_approved", map[string]any{"tool": request["tool_name"]}, "")
		return
	}
	behavior := "allow"
	message := ""
	if subtype != "hook_callback" {
		behavior = "deny"
		message = "Unsupported control request"
	}
	response := map[string]any{"behavior": behavior}
	if message != "" {
		response["message"] = message
	}
	if err := s.writeJSON(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": response}}); err != nil {
		s.emit("error", map[string]any{"message": err.Error()}, "")
	}
}

// readStderr records diagnostic lines separately from the JSON protocol stream.
func (s *Session) readStderr(reader io.Reader) {
	defer close(s.stderrDone)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_ = appendLine(filepath.Join(s.logRoot, s.info.ID+".stderr.log"), line)
		s.emit("stderr", nil, line)
	}
	if err := scanner.Err(); err != nil && !isClosedPipeError(err) {
		s.emit("error", map[string]any{"message": err.Error()}, "")
	}
}

// wait lets both pipe readers drain before Cmd.Wait closes their descriptors,
// then records the final process exit code for polling and archived diagnostics.
func (s *Session) wait() {
	<-s.stdoutDone
	<-s.stderrDone
	err := s.cmd.Wait()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	s.eventMu.Lock()
	s.info.Status = "EXITED"
	s.info.Busy = false
	intentional := s.stopRequested
	s.eventMu.Unlock()
	s.emit("process_exit", map[string]any{"code": exitCode, "intentional": intentional}, "")
	s.logger.Info("claude session exited", "session_id", s.info.ID, "task_id", s.info.TaskID, "member_id", s.info.MemberID, "code", exitCode)
}

// emit appends one bounded, ordered event and mirrors it to a unified durable log.
// Disk persistence happens after releasing eventMu so a slow filesystem cannot
// block browser readers or process-status updates.
func (s *Session) emit(eventType string, payload any, line string) {
	s.eventMu.Lock()
	s.sequence++
	event := Event{Sequence: s.sequence, Type: eventType, Payload: payload, Line: line, CreatedAt: time.Now().UTC()}
	if retainForPolling(event) {
		s.events = append(s.events, event)
		if len(s.events) > 2000 {
			s.events = append([]Event(nil), s.events[len(s.events)-1500:]...)
		}
	}
	sessionID := s.info.ID
	s.eventMu.Unlock()
	if encoded, err := json.Marshal(event); err != nil {
		s.logger.Error("failed to encode runtime event", "error", err, "session_id", sessionID)
	} else if err := appendLine(filepath.Join(s.logRoot, sessionID+".events.ndjson"), string(encoded)); err != nil {
		s.logger.Error("failed to persist runtime event", "error", err, "session_id", sessionID)
	}
}

// retainForPolling drops raw token protocol envelopes from bounded memory. The
// compact assistant_delta events emitted above remain available to live clients.
func retainForPolling(event Event) bool {
	if event.Type != "claude_event" {
		return true
	}
	payload, ok := event.Payload.(map[string]any)
	return !ok || payload["type"] != "stream_event"
}

// isClosedPipeError recognizes the benign Windows descriptor error produced
// during process shutdown so it is not presented as an agent failure.
func isClosedPipeError(err error) bool {
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file already closed") || strings.Contains(message, "pipe has been ended")
}

// IsClosedPipeError exposes the stale-pipe check to the HTTP layer so message
// routing can recover by starting a fresh member process without surfacing noise.
func IsClosedPipeError(err error) bool {
	return isClosedPipeError(err)
}

// buildCommand creates a cross-platform Claude command compatible with npm Windows shims.
func buildCommand(ctx context.Context, args []string) *exec.Cmd {
	if gort.GOOS == "windows" {
		all := append([]string{"/C", "claude"}, args...)
		return exec.CommandContext(ctx, "cmd", all...)
	}
	return exec.CommandContext(ctx, "claude", args...)
}

// appendLine writes one diagnostic or protocol line to a task-local log file.
func appendLine(path string, line string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(line + "\n")
	return err
}

// memberKey builds an unambiguous task-member lookup key.
func memberKey(taskID string, memberID string) string {
	return taskID + "\x00" + memberID
}

// newSessionID creates a collision-resistant route and control request ID.
func newSessionID() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "ses_" + time.Now().UTC().Format("20060102_150405") + "_" + hex.EncodeToString(buffer), nil
}
