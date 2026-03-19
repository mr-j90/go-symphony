package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/jordan/go-symphony/internal/model"
)

// ClaudeRunnerConfig holds Claude Code-specific configuration.
type ClaudeRunnerConfig struct {
	// Command is the base command (default: "claude")
	Command string

	// Model to use (e.g., "opus", "sonnet", "haiku", or full model ID)
	Model string

	// MaxTurns limits how many turns Claude Code can take per invocation
	MaxTurns int

	// AllowedTools is the list of tools to auto-approve (e.g., "Bash", "Read", "Edit", "Write")
	AllowedTools []string

	// DisallowedTools restricts specific tools
	DisallowedTools []string

	// PermissionMode controls Claude's permission behavior ("default", "plan", "auto", "bypassPermissions")
	PermissionMode string

	// DangerouslySkipPermissions auto-approves everything (use with caution)
	DangerouslySkipPermissions bool

	// AppendSystemPrompt adds additional system-level instructions
	AppendSystemPrompt string

	// TurnTimeoutMS is the total timeout for one invocation
	TurnTimeoutMS int

	// MaxBudgetUSD caps spending per invocation
	MaxBudgetUSD float64
}

// ClaudeRunner manages Claude Code sessions.
type ClaudeRunner struct {
	config ClaudeRunnerConfig
	logger *slog.Logger
}

// NewClaudeRunner creates a new Claude Code runner.
func NewClaudeRunner(cfg ClaudeRunnerConfig, logger *slog.Logger) *ClaudeRunner {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.TurnTimeoutMS == 0 {
		cfg.TurnTimeoutMS = 3600000 // 1 hour default
	}
	return &ClaudeRunner{
		config: cfg,
		logger: logger,
	}
}

// UpdateClaudeConfig updates the runner config (for dynamic reload).
func (r *ClaudeRunner) UpdateClaudeConfig(cfg ClaudeRunnerConfig) {
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.TurnTimeoutMS == 0 {
		cfg.TurnTimeoutMS = 3600000
	}
	r.config = cfg
}

// ClaudeSession represents a Claude Code session (may span multiple invocations via --resume).
type ClaudeSession struct {
	sessionID string
	runner    *ClaudeRunner
	logger    *slog.Logger
}

// StartClaudeSession creates a new session (the actual process is started per-turn).
func (r *ClaudeRunner) StartClaudeSession(ctx context.Context, workspacePath string) (*ClaudeSession, error) {
	// Verify the command exists
	_, err := exec.LookPath(r.config.Command)
	if err != nil {
		// Try via bash
		testCmd := exec.CommandContext(ctx, "bash", "-lc", fmt.Sprintf("which %s", r.config.Command)) //nolint:gosec // command is from trusted config
		if testErr := testCmd.Run(); testErr != nil {
			return nil, fmt.Errorf("claude_not_found: %s is not available in PATH: %w", r.config.Command, err)
		}
	}

	return &ClaudeSession{
		runner: r,
		logger: r.logger,
	}, nil
}

// ClaudeResult is the JSON output from `claude -p --output-format json`.
type ClaudeResult struct {
	Result    string  `json:"result"`
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd"`
	Duration  float64 `json:"duration_seconds"`
	TurnCount int     `json:"num_turns"`
}

// ClaudeStreamEvent is one line from `claude -p --output-format stream-json`.
type ClaudeStreamEvent struct {
	Type    string          `json:"type"`
	Event   json.RawMessage `json:"event,omitempty"`
	Message string          `json:"message,omitempty"`

	// Parsed fields for specific event types
	SessionID string `json:"session_id,omitempty"`
	Result    string `json:"result,omitempty"`
}

// RunTurn executes a Claude Code invocation for this session.
// On the first call, it starts a new session. On subsequent calls, it resumes the session.
func (s *ClaudeSession) RunTurn(ctx context.Context, prompt string, issue model.Issue, turnNum int, workspacePath string, onEvent EventCallback) error {
	timeout := time.Duration(s.runner.config.TurnTimeoutMS) * time.Millisecond
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := s.buildArgs(prompt, turnNum)

	s.logger.Info("launching claude",
		"command", s.runner.config.Command,
		"workspace", workspacePath,
		"turn_num", turnNum,
		"session_id", s.sessionID,
	)

	cmd := exec.CommandContext(turnCtx, "bash", "-lc", s.runner.config.Command+" "+strings.Join(args, " ")) //nolint:gosec // command is from trusted config
	cmd.Dir = workspacePath

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("claude stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("claude stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude_not_found: %w", err)
	}

	// Drain stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
		for scanner.Scan() {
			s.logger.Debug("claude stderr", "line", scanner.Text())
		}
	}()

	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-turn-%d", turnNum)
	}

	// Emit session started event
	if onEvent != nil {
		onEvent(model.CodexEvent{
			Event:     "session_started",
			Timestamp: time.Now().UTC(),
			SessionID: sessionID,
		})
	}

	// Stream JSON events
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event ClaudeStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Not valid JSON, may be plain text output
			if onEvent != nil {
				onEvent(model.CodexEvent{
					Event:     "notification",
					Timestamp: time.Now().UTC(),
					Message:   truncate(line, 200),
					SessionID: sessionID,
				})
			}
			continue
		}

		// Extract session ID from first event that has it
		if event.SessionID != "" && s.sessionID == "" {
			s.sessionID = event.SessionID
			sessionID = s.sessionID
		}

		// Forward events to orchestrator
		if onEvent != nil {
			ce := model.CodexEvent{
				Event:     classifyClaudeEvent(event),
				Timestamp: time.Now().UTC(),
				Message:   extractClaudeMessage(event),
				SessionID: sessionID,
				Usage:     extractClaudeUsage([]byte(line)),
			}
			onEvent(ce)
		}
	}

	// Wait for process to finish
	if err := cmd.Wait(); err != nil {
		if turnCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("turn_timeout")
		}
		if turnCtx.Err() == context.Canceled {
			return turnCtx.Err()
		}
		return fmt.Errorf("turn_failed: claude exited with error: %w", err)
	}

	if onEvent != nil {
		onEvent(model.CodexEvent{
			Event:     "turn_completed",
			Timestamp: time.Now().UTC(),
			SessionID: sessionID,
		})
	}

	return nil
}

func (s *ClaudeSession) buildArgs(prompt string, turnNum int) []string {
	args := []string{"-p", shellescape(prompt)}

	args = append(args, "--output-format", "stream-json")
	args = append(args, "--verbose")

	if s.runner.config.Model != "" {
		args = append(args, "--model", s.runner.config.Model)
	}

	if s.runner.config.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", s.runner.config.MaxTurns))
	}

	if s.runner.config.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", s.runner.config.MaxBudgetUSD))
	}

	if s.runner.config.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else if s.runner.config.PermissionMode != "" {
		args = append(args, "--permission-mode", s.runner.config.PermissionMode)
	}

	for _, tool := range s.runner.config.AllowedTools {
		args = append(args, "--allowedTools", shellescape(tool))
	}
	for _, tool := range s.runner.config.DisallowedTools {
		args = append(args, "--disallowedTools", shellescape(tool))
	}

	if s.runner.config.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", shellescape(s.runner.config.AppendSystemPrompt))
	}

	// Resume existing session for continuation turns
	if turnNum > 1 && s.sessionID != "" {
		args = append(args, "--resume", s.sessionID)
	}

	return args
}

// Stop is a no-op for Claude sessions (process exits on its own).
func (s *ClaudeSession) Stop() {}

// SessionID returns the Claude Code session ID.
func (s *ClaudeSession) SessionID() string {
	return s.sessionID
}

func classifyClaudeEvent(event ClaudeStreamEvent) string {
	switch event.Type {
	case "result":
		return "turn_completed"
	case "error":
		return "turn_failed"
	case "stream_event":
		return "notification"
	case "tool_use":
		return "notification"
	default:
		return "other_message"
	}
}

func extractClaudeMessage(event ClaudeStreamEvent) string {
	if event.Message != "" {
		return truncate(event.Message, 200)
	}
	if event.Result != "" {
		return truncate(event.Result, 200)
	}
	return event.Type
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

func shellescape(s string) string {
	// Wrap in single quotes and escape any single quotes within
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// extractClaudeUsage parses token usage from a raw Claude stream-json line.
// Claude emits usage in two shapes:
//
//	result events:    {"type":"result", "usage":{"input_tokens":N,"output_tokens":N,...}}
//	assistant events: {"type":"assistant","message":{"usage":{"input_tokens":N,"output_tokens":N,...}}}
func extractClaudeUsage(raw []byte) *model.TokenUsage {
	// Use a flexible struct that captures both shapes without conflicting tags.
	var envelope struct {
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Message *struct {
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}

	var input, output int64

	if envelope.Usage != nil {
		input = envelope.Usage.InputTokens
		output = envelope.Usage.OutputTokens
	} else if envelope.Message != nil && envelope.Message.Usage != nil {
		input = envelope.Message.Usage.InputTokens
		output = envelope.Message.Usage.OutputTokens
	}

	if input == 0 && output == 0 {
		return nil
	}
	return &model.TokenUsage{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
	}
}
