package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config holds typed runtime configuration derived from WORKFLOW.md front matter.
type Config struct {
	mu sync.RWMutex

	// Tracker
	TrackerKind           string
	TrackerEndpoint       string
	TrackerAPIKey         string
	TrackerProjectSlug    string
	TrackerActiveStates   []string
	TrackerTerminalStates []string

	// Polling
	PollIntervalMS int

	// Workspace
	WorkspaceRoot string

	// Hooks
	HookAfterCreate  string
	HookBeforeRun    string
	HookAfterRun     string
	HookBeforeRemove string
	HookTimeoutMS    int

	// Agent
	MaxConcurrentAgents  int
	MaxRetryBackoffMS    int
	MaxTurns             int
	MaxConcurrentByState map[string]int // normalized lowercase keys

	// Dispatch state transition: if set, move issue to this state on dispatch
	DispatchTransitionState string

	// Agent type: "codex" (default) or "claude_code"
	AgentType string

	// Codex
	CodexCommand           string
	CodexApprovalPolicy    string
	CodexThreadSandbox     string
	CodexTurnSandboxPolicy string
	CodexTurnTimeoutMS     int
	CodexReadTimeoutMS     int
	CodexStallTimeoutMS    int

	// Claude Code
	ClaudeCommand                    string
	ClaudeModel                      string
	ClaudeMaxTurns                   int
	ClaudeAllowedTools               []string
	ClaudeDisallowedTools            []string
	ClaudePermissionMode             string
	ClaudeDangerouslySkipPermissions bool
	ClaudeAppendSystemPrompt         string
	ClaudeTurnTimeoutMS              int
	ClaudeMaxBudgetUSD               float64

	// Server (extension)
	ServerPort *int
}

// DefaultConfig returns a Config with all spec defaults applied.
func DefaultConfig() *Config {
	return &Config{
		TrackerKind:           "",
		TrackerEndpoint:       "",
		TrackerAPIKey:         "",
		TrackerProjectSlug:    "",
		TrackerActiveStates:   []string{"Todo", "In Progress"},
		TrackerTerminalStates: []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}, //nolint:misspell // "Cancelled" is a real Linear workflow state name

		PollIntervalMS: 30000,

		WorkspaceRoot: filepath.Join(os.TempDir(), "symphony_workspaces"),

		HookTimeoutMS: 60000,

		MaxConcurrentAgents:  10,
		MaxRetryBackoffMS:    300000,
		MaxTurns:             20,
		MaxConcurrentByState: map[string]int{},

		AgentType: "codex",

		CodexCommand:        "codex app-server",
		CodexTurnTimeoutMS:  3600000,
		CodexReadTimeoutMS:  5000,
		CodexStallTimeoutMS: 300000,

		ClaudeCommand:       "claude",
		ClaudeTurnTimeoutMS: 3600000,
	}
}

// LoadFromMap applies WORKFLOW.md front matter values onto defaults.
func LoadFromMap(fm map[string]any) (*Config, error) {
	c := DefaultConfig()

	if tracker, ok := getMap(fm, "tracker"); ok {
		if v, ok := getStr(tracker, "kind"); ok {
			c.TrackerKind = v
		}
		if v, ok := getStr(tracker, "endpoint"); ok {
			c.TrackerEndpoint = v
		}
		if v, ok := getStr(tracker, "api_key"); ok {
			c.TrackerAPIKey = resolveEnv(v)
		}
		if v, ok := getStr(tracker, "project_slug"); ok {
			c.TrackerProjectSlug = v
		}
		if v, ok := getStrSlice(tracker, "active_states"); ok {
			c.TrackerActiveStates = v
		}
		if v, ok := getStrSlice(tracker, "terminal_states"); ok {
			c.TrackerTerminalStates = v
		}
	}

	// Apply default endpoint for linear
	if c.TrackerKind == "linear" && c.TrackerEndpoint == "" {
		c.TrackerEndpoint = "https://api.linear.app/graphql"
	}

	// Resolve API key from canonical env var if not set
	if c.TrackerKind == "linear" && c.TrackerAPIKey == "" {
		c.TrackerAPIKey = os.Getenv("LINEAR_API_KEY")
	}

	if polling, ok := getMap(fm, "polling"); ok {
		if v, ok := getInt(polling, "interval_ms"); ok {
			c.PollIntervalMS = v
		}
	}

	if ws, ok := getMap(fm, "workspace"); ok {
		if v, ok := getStr(ws, "root"); ok {
			c.WorkspaceRoot = expandPath(resolveEnv(v))
		}
	}

	if hooks, ok := getMap(fm, "hooks"); ok {
		if v, ok := getStr(hooks, "after_create"); ok {
			c.HookAfterCreate = v
		}
		if v, ok := getStr(hooks, "before_run"); ok {
			c.HookBeforeRun = v
		}
		if v, ok := getStr(hooks, "after_run"); ok {
			c.HookAfterRun = v
		}
		if v, ok := getStr(hooks, "before_remove"); ok {
			c.HookBeforeRemove = v
		}
		if v, ok := getInt(hooks, "timeout_ms"); ok && v > 0 {
			c.HookTimeoutMS = v
		}
	}

	if agent, ok := getMap(fm, "agent"); ok {
		if v, ok := getStr(agent, "type"); ok {
			c.AgentType = v
		}
		if v, ok := getStr(agent, "dispatch_transition_state"); ok {
			c.DispatchTransitionState = v
		}
		if v, ok := getInt(agent, "max_concurrent_agents"); ok {
			c.MaxConcurrentAgents = v
		}
		if v, ok := getInt(agent, "max_retry_backoff_ms"); ok {
			c.MaxRetryBackoffMS = v
		}
		if v, ok := getInt(agent, "max_turns"); ok {
			c.MaxTurns = v
		}
		if byState, ok := getMap(agent, "max_concurrent_agents_by_state"); ok {
			m := map[string]int{}
			for k, val := range byState {
				if n, ok := toInt(val); ok && n > 0 {
					m[strings.ToLower(k)] = n
				}
			}
			c.MaxConcurrentByState = m
		}
	}

	// Claude Code config
	if cc, ok := getMap(fm, "claude_code"); ok {
		if v, ok := getStr(cc, "command"); ok {
			c.ClaudeCommand = v
		}
		if v, ok := getStr(cc, "model"); ok {
			c.ClaudeModel = v
		}
		if v, ok := getInt(cc, "max_turns"); ok {
			c.ClaudeMaxTurns = v
		}
		if v, ok := getStrSlice(cc, "allowed_tools"); ok {
			c.ClaudeAllowedTools = v
		}
		if v, ok := getStrSlice(cc, "disallowed_tools"); ok {
			c.ClaudeDisallowedTools = v
		}
		if v, ok := getStr(cc, "permission_mode"); ok {
			c.ClaudePermissionMode = v
		}
		if v, ok := getBool(cc, "dangerously_skip_permissions"); ok && v {
			c.ClaudeDangerouslySkipPermissions = true
		}
		if v, ok := getStr(cc, "append_system_prompt"); ok {
			c.ClaudeAppendSystemPrompt = v
		}
		if v, ok := getInt(cc, "turn_timeout_ms"); ok {
			c.ClaudeTurnTimeoutMS = v
		}
		if v, ok := getFloat(cc, "max_budget_usd"); ok {
			c.ClaudeMaxBudgetUSD = v
		}
	}

	if codex, ok := getMap(fm, "codex"); ok {
		if v, ok := getStr(codex, "command"); ok {
			c.CodexCommand = v
		}
		if v, ok := getStr(codex, "approval_policy"); ok {
			c.CodexApprovalPolicy = v
		}
		if v, ok := getStr(codex, "thread_sandbox"); ok {
			c.CodexThreadSandbox = v
		}
		if v, ok := getStr(codex, "turn_sandbox_policy"); ok {
			c.CodexTurnSandboxPolicy = v
		}
		if v, ok := getInt(codex, "turn_timeout_ms"); ok {
			c.CodexTurnTimeoutMS = v
		}
		if v, ok := getInt(codex, "read_timeout_ms"); ok {
			c.CodexReadTimeoutMS = v
		}
		if v, ok := getInt(codex, "stall_timeout_ms"); ok {
			c.CodexStallTimeoutMS = v
		}
	}

	if server, ok := getMap(fm, "server"); ok {
		if v, ok := getInt(server, "port"); ok {
			c.ServerPort = &v
		}
	}

	return c, nil
}

// PollInterval returns the poll interval as a Duration.
func (c *Config) PollInterval() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.PollIntervalMS) * time.Millisecond
}

// HookTimeout returns the hook timeout as a Duration.
func (c *Config) HookTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.HookTimeoutMS) * time.Millisecond
}

// TurnTimeout returns the codex turn timeout as a Duration.
func (c *Config) TurnTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.CodexTurnTimeoutMS) * time.Millisecond
}

// ReadTimeout returns the codex read timeout as a Duration.
func (c *Config) ReadTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.CodexReadTimeoutMS) * time.Millisecond
}

// StallTimeout returns the codex stall timeout as a Duration.
func (c *Config) StallTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.CodexStallTimeoutMS) * time.Millisecond
}

// MaxRetryBackoff returns the max retry backoff as a Duration.
func (c *Config) MaxRetryBackoff() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Duration(c.MaxRetryBackoffMS) * time.Millisecond
}

// ValidateForDispatch checks that required fields are present for dispatch.
func (c *Config) ValidateForDispatch() error {
	if c.TrackerKind == "" {
		return fmt.Errorf("tracker.kind is required")
	}
	if c.TrackerKind != "linear" {
		return fmt.Errorf("unsupported tracker.kind: %s", c.TrackerKind)
	}
	if c.TrackerAPIKey == "" {
		return fmt.Errorf("tracker.api_key is required (set LINEAR_API_KEY or use $VAR in workflow)")
	}
	if c.TrackerProjectSlug == "" {
		return fmt.Errorf("tracker.project_slug is required for linear tracker")
	}
	switch c.AgentType {
	case "codex":
		if c.CodexCommand == "" {
			return fmt.Errorf("codex.command is required")
		}
	case "claude_code":
		if c.ClaudeCommand == "" {
			return fmt.Errorf("claude_code.command is required")
		}
	default:
		return fmt.Errorf("unsupported agent.type: %q (use \"codex\" or \"claude_code\")", c.AgentType)
	}
	return nil
}

// IsActiveState checks if a state name is in the active states list.
func (c *Config) IsActiveState(state string) bool {
	lower := strings.ToLower(state)
	for _, s := range c.TrackerActiveStates {
		if strings.ToLower(s) == lower {
			return true
		}
	}
	return false
}

// IsTerminalState checks if a state name is in the terminal states list.
func (c *Config) IsTerminalState(state string) bool {
	lower := strings.ToLower(state)
	for _, s := range c.TrackerTerminalStates {
		if strings.ToLower(s) == lower {
			return true
		}
	}
	return false
}

// helpers

func resolveEnv(v string) string {
	if strings.HasPrefix(v, "$") {
		envVal := os.Getenv(v[1:])
		return envVal
	}
	return v
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	return p
}

func getMap(m map[string]any, key string) (map[string]any, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	mm, ok := v.(map[string]any)
	return mm, ok
}

func getStr(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getStrSlice(m map[string]any, key string) ([]string, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	case []string:
		return val, true
	}
	return nil, false
}

func getInt(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	return toInt(v)
}

func toInt(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case string:
		n, err := strconv.Atoi(val)
		return n, err == nil
	}
	return 0, false
}

func getBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func getFloat(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	}
	return 0, false
}
