package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jordan/go-symphony/internal/model"
)

// EventCallback is called for each agent event.
type EventCallback func(event model.CodexEvent)

// RunnerConfig holds the agent runner's configuration.
type RunnerConfig struct {
	Command          string
	ApprovalPolicy   string
	ThreadSandbox    string
	TurnSandboxPolicy string
	TurnTimeoutMS    int
	ReadTimeoutMS    int
}

// Runner manages a Codex app-server session.
type Runner struct {
	config RunnerConfig
	logger *slog.Logger
}

// NewRunner creates a new agent runner.
func NewRunner(cfg RunnerConfig, logger *slog.Logger) *Runner {
	return &Runner{
		config: cfg,
		logger: logger,
	}
}

// UpdateConfig updates the runner config (for dynamic reload).
func (r *Runner) UpdateConfig(cfg RunnerConfig) {
	r.config = cfg
}

// Session represents a live coding agent session.
type Session struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   io.ReadCloser
	threadID string
	turnID   string
	nextID   int
	mu       sync.Mutex
	logger   *slog.Logger
	config   RunnerConfig
	pid      string
}

// StartSession launches the app-server process and performs the startup handshake.
func (r *Runner) StartSession(ctx context.Context, workspacePath string) (*Session, error) {
	r.logger.Info("launching codex app-server",
		"command", r.config.Command,
		"workspace", workspacePath,
	)

	cmd := exec.CommandContext(ctx, "bash", "-lc", r.config.Command)
	cmd.Dir = workspacePath

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex_not_found: %w", err)
	}

	s := &Session{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
		stderr: stderr,
		nextID: 1,
		logger: r.logger,
		config: r.config,
	}

	if cmd.Process != nil {
		s.pid = fmt.Sprintf("%d", cmd.Process.Pid)
	}

	// Set max line size to 10MB
	s.stdout.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// Drain stderr in background
	go s.drainStderr()

	// Startup handshake
	if err := s.handshake(ctx, workspacePath); err != nil {
		s.Stop()
		return nil, err
	}

	return s, nil
}

func (s *Session) handshake(ctx context.Context, workspacePath string) error {
	readTimeout := time.Duration(s.config.ReadTimeoutMS) * time.Millisecond

	// 1. initialize
	initResp, err := s.sendRequest(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "symphony", "version": "1.0"},
		"capabilities": map[string]any{},
	}, readTimeout)
	if err != nil {
		return fmt.Errorf("startup_failed: initialize: %w", err)
	}
	_ = initResp

	// 2. initialized notification
	if err := s.sendNotification("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("startup_failed: initialized notification: %w", err)
	}

	// 3. thread/start
	threadParams := map[string]any{
		"cwd": workspacePath,
	}
	if s.config.ApprovalPolicy != "" {
		threadParams["approvalPolicy"] = s.config.ApprovalPolicy
	}
	if s.config.ThreadSandbox != "" {
		threadParams["sandbox"] = s.config.ThreadSandbox
	}

	threadResp, err := s.sendRequest(ctx, "thread/start", threadParams, readTimeout)
	if err != nil {
		return fmt.Errorf("startup_failed: thread/start: %w", err)
	}

	// Extract thread ID
	var threadResult struct {
		Result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		} `json:"result"`
	}
	if err := json.Unmarshal(threadResp, &threadResult); err == nil {
		s.threadID = threadResult.Result.Thread.ID
	}
	if s.threadID == "" {
		return fmt.Errorf("startup_failed: no thread ID in thread/start response")
	}

	s.logger.Info("session started", "thread_id", s.threadID, "pid", s.pid)
	return nil
}

// RunTurn starts a new turn and streams events until completion.
func (s *Session) RunTurn(ctx context.Context, prompt string, issue model.Issue, turnNum int, onEvent EventCallback) error {
	readTimeout := time.Duration(s.config.ReadTimeoutMS) * time.Millisecond
	turnTimeout := time.Duration(s.config.TurnTimeoutMS) * time.Millisecond

	turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)
	defer turnCancel()

	// Build turn/start params
	turnParams := map[string]any{
		"threadId": s.threadID,
		"input": []map[string]any{
			{"type": "text", "text": prompt},
		},
		"cwd":   s.cmd.Dir,
		"title": fmt.Sprintf("%s: %s", issue.Identifier, issue.Title),
	}
	if s.config.ApprovalPolicy != "" {
		turnParams["approvalPolicy"] = s.config.ApprovalPolicy
	}
	if s.config.TurnSandboxPolicy != "" {
		turnParams["sandboxPolicy"] = map[string]any{
			"type": s.config.TurnSandboxPolicy,
		}
	}

	turnResp, err := s.sendRequest(turnCtx, "turn/start", turnParams, readTimeout)
	if err != nil {
		return fmt.Errorf("turn_failed: turn/start: %w", err)
	}

	// Extract turn ID
	var turnResult struct {
		Result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		} `json:"result"`
	}
	if err := json.Unmarshal(turnResp, &turnResult); err == nil {
		s.turnID = turnResult.Result.Turn.ID
	}

	sessionID := s.threadID + "-" + s.turnID
	s.logger.Info("turn started",
		"session_id", sessionID,
		"turn_num", turnNum,
	)

	if onEvent != nil {
		onEvent(model.CodexEvent{
			Event:             "session_started",
			Timestamp:         time.Now().UTC(),
			CodexAppServerPID: s.pid,
			SessionID:         sessionID,
			ThreadID:          s.threadID,
			TurnID:            s.turnID,
		})
	}

	// Stream events until turn completes
	return s.streamTurn(turnCtx, sessionID, onEvent)
}

func (s *Session) streamTurn(ctx context.Context, sessionID string, onEvent EventCallback) error {
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("turn_timeout")
			}
			return ctx.Err()
		default:
		}

		if !s.stdout.Scan() {
			if err := s.stdout.Err(); err != nil {
				return fmt.Errorf("port_exit: stdout read error: %w", err)
			}
			return fmt.Errorf("port_exit: stdout closed")
		}

		line := s.stdout.Text()
		if line == "" {
			continue
		}

		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			if onEvent != nil {
				onEvent(model.CodexEvent{
					Event:     "malformed",
					Timestamp: time.Now().UTC(),
					Message:   line,
					SessionID: sessionID,
				})
			}
			continue
		}

		method, _ := msg["method"].(string)
		event := s.classifyMessage(method, msg)

		if onEvent != nil {
			event.Timestamp = time.Now().UTC()
			event.SessionID = sessionID
			event.CodexAppServerPID = s.pid
			event.Usage = extractUsage(msg)
			onEvent(event)
		}

		// Handle specific protocol messages
		switch method {
		case "turn/completed":
			return nil
		case "turn/failed":
			errMsg := extractErrorMessage(msg)
			return fmt.Errorf("turn_failed: %s", errMsg)
		case "turn/cancelled":
			return fmt.Errorf("turn_cancelled")
		}

		// Check for approval requests - auto-approve
		if s.isApprovalRequest(msg) {
			s.handleApproval(msg)
		}

		// Check for user input required - hard fail
		if s.isUserInputRequired(method, msg) {
			return fmt.Errorf("turn_input_required")
		}

		// Handle unsupported tool calls
		if s.isToolCall(method) {
			s.handleUnsupportedTool(msg)
		}
	}
}

func (s *Session) classifyMessage(method string, msg map[string]any) model.CodexEvent {
	switch {
	case method == "turn/completed":
		return model.CodexEvent{Event: "turn_completed"}
	case method == "turn/failed":
		return model.CodexEvent{Event: "turn_failed", Error: extractErrorMessage(msg)}
	case method == "turn/cancelled":
		return model.CodexEvent{Event: "turn_cancelled"}
	case strings.Contains(method, "approval"):
		return model.CodexEvent{Event: "approval_auto_approved", Message: method}
	case strings.Contains(method, "notification") || method == "":
		summary := extractMessageSummary(msg)
		return model.CodexEvent{Event: "notification", Message: summary}
	default:
		return model.CodexEvent{Event: "other_message", Message: method}
	}
}

func (s *Session) isApprovalRequest(msg map[string]any) bool {
	method, _ := msg["method"].(string)
	if strings.Contains(method, "approval") {
		return true
	}
	// Check for approval request pattern in params
	if params, ok := msg["params"].(map[string]any); ok {
		if _, ok := params["approvalRequest"]; ok {
			return true
		}
	}
	return false
}

func (s *Session) handleApproval(msg map[string]any) {
	id := msg["id"]
	if id == nil {
		return
	}
	resp := map[string]any{
		"id":     id,
		"result": map[string]any{"approved": true},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Warn("failed to marshal approval response", "error", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.stdin, "%s\n", data); err != nil {
		s.logger.Warn("failed to send approval", "error", err)
	}
}

func (s *Session) isUserInputRequired(method string, msg map[string]any) bool {
	if method == "item/tool/requestUserInput" {
		return true
	}
	if params, ok := msg["params"].(map[string]any); ok {
		if req, ok := params["inputRequired"].(bool); ok && req {
			return true
		}
	}
	return false
}

func (s *Session) isToolCall(method string) bool {
	return method == "item/tool/call"
}

func (s *Session) handleUnsupportedTool(msg map[string]any) {
	id := msg["id"]
	if id == nil {
		return
	}
	resp := map[string]any{
		"id":     id,
		"result": map[string]any{"success": false, "error": "unsupported_tool_call"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.stdin, "%s\n", data)
}

func (s *Session) sendRequest(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.mu.Unlock()

	req := map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	s.mu.Lock()
	_, err = fmt.Fprintf(s.stdin, "%s\n", data)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response with timeout
	type readResult struct {
		data json.RawMessage
		err  error
	}
	ch := make(chan readResult, 1)

	go func() {
		for s.stdout.Scan() {
			line := s.stdout.Text()
			if line == "" {
				continue
			}
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			// Check if this is the response to our request
			if msgID, ok := msg["id"]; ok {
				var msgIDInt int
				switch v := msgID.(type) {
				case float64:
					msgIDInt = int(v)
				case int:
					msgIDInt = v
				}
				if msgIDInt == id {
					ch <- readResult{data: []byte(line)}
					return
				}
			}
		}
		ch <- readResult{err: fmt.Errorf("stdout closed while waiting for response")}
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("response_timeout: %w", timeoutCtx.Err())
	case result := <-ch:
		return result.data, result.err
	}
}

func (s *Session) sendNotification(method string, params any) error {
	msg := map[string]any{
		"method": method,
		"params": params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = fmt.Fprintf(s.stdin, "%s\n", data)
	return err
}

// Stop terminates the app-server process.
func (s *Session) Stop() {
	if s.stdin != nil {
		s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	if s.cmd != nil {
		s.cmd.Wait()
	}
}

// ThreadID returns the session's thread ID.
func (s *Session) ThreadID() string {
	return s.threadID
}

func (s *Session) drainStderr() {
	scanner := bufio.NewScanner(s.stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		s.logger.Debug("codex stderr", "line", scanner.Text())
	}
}

func extractUsage(msg map[string]any) *model.TokenUsage {
	// Try several common payload shapes
	if usage := findUsageInMap(msg, "usage"); usage != nil {
		return usage
	}
	if params, ok := msg["params"].(map[string]any); ok {
		if usage := findUsageInMap(params, "usage"); usage != nil {
			return usage
		}
		if usage := findUsageInMap(params, "total_token_usage"); usage != nil {
			return usage
		}
	}
	return nil
}

func findUsageInMap(m map[string]any, key string) *model.TokenUsage {
	v, ok := m[key]
	if !ok {
		return nil
	}
	um, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	usage := &model.TokenUsage{}
	if input, ok := um["input_tokens"].(float64); ok {
		usage.InputTokens = int64(input)
	}
	if output, ok := um["output_tokens"].(float64); ok {
		usage.OutputTokens = int64(output)
	}
	if total, ok := um["total_tokens"].(float64); ok {
		usage.TotalTokens = int64(total)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

func extractErrorMessage(msg map[string]any) string {
	if params, ok := msg["params"].(map[string]any); ok {
		if errMsg, ok := params["error"].(string); ok {
			return errMsg
		}
		if errObj, ok := params["error"].(map[string]any); ok {
			if m, ok := errObj["message"].(string); ok {
				return m
			}
		}
	}
	if err, ok := msg["error"].(map[string]any); ok {
		if m, ok := err["message"].(string); ok {
			return m
		}
	}
	return "unknown error"
}

func extractMessageSummary(msg map[string]any) string {
	if params, ok := msg["params"].(map[string]any); ok {
		if m, ok := params["message"].(string); ok {
			if len(m) > 200 {
				return m[:200]
			}
			return m
		}
		if m, ok := params["text"].(string); ok {
			if len(m) > 200 {
				return m[:200]
			}
			return m
		}
	}
	return ""
}
