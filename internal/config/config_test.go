package config

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()

	if c.PollIntervalMS != 30000 {
		t.Errorf("expected PollIntervalMS=30000, got %d", c.PollIntervalMS)
	}
	if c.MaxConcurrentAgents != 10 {
		t.Errorf("expected MaxConcurrentAgents=10, got %d", c.MaxConcurrentAgents)
	}
	if c.MaxRetryBackoffMS != 300000 {
		t.Errorf("expected MaxRetryBackoffMS=300000, got %d", c.MaxRetryBackoffMS)
	}
	if c.CodexCommand != "codex app-server" {
		t.Errorf("expected CodexCommand='codex app-server', got %q", c.CodexCommand)
	}
	if c.HookTimeoutMS != 60000 {
		t.Errorf("expected HookTimeoutMS=60000, got %d", c.HookTimeoutMS)
	}
	if c.CodexTurnTimeoutMS != 3600000 {
		t.Errorf("expected CodexTurnTimeoutMS=3600000, got %d", c.CodexTurnTimeoutMS)
	}
	if c.CodexStallTimeoutMS != 300000 {
		t.Errorf("expected CodexStallTimeoutMS=300000, got %d", c.CodexStallTimeoutMS)
	}
	if len(c.TrackerActiveStates) != 2 {
		t.Errorf("expected 2 active states, got %d", len(c.TrackerActiveStates))
	}
	if len(c.TrackerTerminalStates) != 5 {
		t.Errorf("expected 5 terminal states, got %d", len(c.TrackerTerminalStates))
	}
}

func TestLoadFromMap_Full(t *testing.T) {
	fm := map[string]any{
		"tracker": map[string]any{
			"kind":          "linear",
			"api_key":       "test-key",
			"project_slug":  "my-proj",
			"active_states": []any{"Todo", "In Progress", "Review"},
		},
		"polling": map[string]any{
			"interval_ms": 5000,
		},
		"workspace": map[string]any{
			"root": "/tmp/test_ws",
		},
		"hooks": map[string]any{
			"after_create": "echo created",
			"before_run":   "echo before",
			"timeout_ms":   30000,
		},
		"agent": map[string]any{
			"max_concurrent_agents": 5,
			"max_retry_backoff_ms":  60000,
			"max_turns":             15,
			"max_concurrent_agents_by_state": map[string]any{
				"Todo":        2,
				"In Progress": 3,
			},
		},
		"codex": map[string]any{
			"command":         "my-codex app-server",
			"turn_timeout_ms": 1800000,
		},
		"server": map[string]any{
			"port": 8080,
		},
	}

	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.TrackerKind != "linear" {
		t.Errorf("expected kind=linear, got %q", c.TrackerKind)
	}
	if c.TrackerAPIKey != "test-key" {
		t.Errorf("expected api_key=test-key, got %q", c.TrackerAPIKey)
	}
	if c.TrackerProjectSlug != "my-proj" {
		t.Errorf("expected project_slug=my-proj, got %q", c.TrackerProjectSlug)
	}
	if len(c.TrackerActiveStates) != 3 {
		t.Errorf("expected 3 active states, got %d", len(c.TrackerActiveStates))
	}
	if c.PollIntervalMS != 5000 {
		t.Errorf("expected poll=5000, got %d", c.PollIntervalMS)
	}
	if c.WorkspaceRoot != "/tmp/test_ws" {
		t.Errorf("expected root=/tmp/test_ws, got %q", c.WorkspaceRoot)
	}
	if c.HookAfterCreate != "echo created" {
		t.Errorf("expected hook, got %q", c.HookAfterCreate)
	}
	if c.HookTimeoutMS != 30000 {
		t.Errorf("expected timeout=30000, got %d", c.HookTimeoutMS)
	}
	if c.MaxConcurrentAgents != 5 {
		t.Errorf("expected max_concurrent=5, got %d", c.MaxConcurrentAgents)
	}
	if c.MaxRetryBackoffMS != 60000 {
		t.Errorf("expected max_retry_backoff=60000, got %d", c.MaxRetryBackoffMS)
	}
	if c.MaxTurns != 15 {
		t.Errorf("expected max_turns=15, got %d", c.MaxTurns)
	}
	if c.MaxConcurrentByState["todo"] != 2 {
		t.Errorf("expected todo=2, got %d", c.MaxConcurrentByState["todo"])
	}
	if c.MaxConcurrentByState["in progress"] != 3 {
		t.Errorf("expected in progress=3, got %d", c.MaxConcurrentByState["in progress"])
	}
	if c.CodexCommand != "my-codex app-server" {
		t.Errorf("expected command, got %q", c.CodexCommand)
	}
	if c.CodexTurnTimeoutMS != 1800000 {
		t.Errorf("expected turn_timeout=1800000, got %d", c.CodexTurnTimeoutMS)
	}
	if c.TrackerEndpoint != "https://api.linear.app/graphql" {
		t.Errorf("expected default endpoint, got %q", c.TrackerEndpoint)
	}
	if c.ServerPort == nil || *c.ServerPort != 8080 {
		t.Error("expected server port 8080")
	}
}

func TestLoadFromMap_EnvVarResolution(t *testing.T) {
	os.Setenv("TEST_LINEAR_KEY", "env-secret")
	defer os.Unsetenv("TEST_LINEAR_KEY")

	fm := map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "$TEST_LINEAR_KEY",
			"project_slug": "proj",
		},
	}

	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.TrackerAPIKey != "env-secret" {
		t.Errorf("expected env-secret, got %q", c.TrackerAPIKey)
	}
}

func TestLoadFromMap_EmptyEnvVar(t *testing.T) {
	os.Setenv("EMPTY_KEY", "")
	defer os.Unsetenv("EMPTY_KEY")

	fm := map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "$EMPTY_KEY",
			"project_slug": "proj",
		},
	}

	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty env var treated as missing
	if c.TrackerAPIKey != "" {
		t.Errorf("expected empty key, got %q", c.TrackerAPIKey)
	}
}

func TestValidateForDispatch(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{"valid", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
			c.TrackerProjectSlug = "proj"
		}, false},
		{"missing kind", func(c *Config) {}, true},
		{"unsupported kind", func(c *Config) { c.TrackerKind = "jira" }, true},
		{"missing api_key", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerProjectSlug = "proj"
		}, true},
		{"missing project_slug", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
		}, true},
		{"empty codex command", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
			c.TrackerProjectSlug = "proj"
			c.CodexCommand = ""
		}, true},
		{"valid claude_code", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
			c.TrackerProjectSlug = "proj"
			c.AgentType = "claude_code"
		}, false},
		{"claude_code empty command", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
			c.TrackerProjectSlug = "proj"
			c.AgentType = "claude_code"
			c.ClaudeCommand = ""
		}, true},
		{"unsupported agent type", func(c *Config) {
			c.TrackerKind = "linear"
			c.TrackerAPIKey = "key"
			c.TrackerProjectSlug = "proj"
			c.AgentType = "unknown"
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := DefaultConfig()
			tt.modify(c)
			err := c.ValidateForDispatch()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateForDispatch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsActiveState(t *testing.T) {
	c := DefaultConfig()
	if !c.IsActiveState("Todo") {
		t.Error("Todo should be active")
	}
	if !c.IsActiveState("todo") {
		t.Error("todo (lowercase) should be active")
	}
	if !c.IsActiveState("In Progress") {
		t.Error("In Progress should be active")
	}
	if c.IsActiveState("Done") {
		t.Error("Done should not be active")
	}
}

func TestIsTerminalState(t *testing.T) {
	c := DefaultConfig()
	if !c.IsTerminalState("Done") {
		t.Error("Done should be terminal")
	}
	if !c.IsTerminalState("done") {
		t.Error("done (lowercase) should be terminal")
	}
	if !c.IsTerminalState("Cancelled") {
		t.Error("Cancelled should be terminal")
	}
	if !c.IsTerminalState("Canceled") {
		t.Error("Canceled should be terminal")
	}
	if c.IsTerminalState("Todo") {
		t.Error("Todo should not be terminal")
	}
}

func TestLoadFromMap_ClaudeCode(t *testing.T) {
	fm := map[string]any{
		"agent": map[string]any{
			"type": "claude_code",
		},
		"claude_code": map[string]any{
			"command":                      "my-claude",
			"model":                        "opus",
			"max_turns":                    15,
			"permission_mode":              "auto",
			"allowed_tools":                []any{"Bash", "Read", "Edit"},
			"turn_timeout_ms":              1800000,
			"max_budget_usd":               5.0,
			"dangerously_skip_permissions": true,
			"append_system_prompt":         "Be careful.",
		},
	}

	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.AgentType != "claude_code" {
		t.Errorf("expected agent type claude_code, got %q", c.AgentType)
	}
	if c.ClaudeCommand != "my-claude" {
		t.Errorf("expected command my-claude, got %q", c.ClaudeCommand)
	}
	if c.ClaudeModel != "opus" {
		t.Errorf("expected model opus, got %q", c.ClaudeModel)
	}
	if c.ClaudeMaxTurns != 15 {
		t.Errorf("expected max_turns 15, got %d", c.ClaudeMaxTurns)
	}
	if c.ClaudePermissionMode != "auto" {
		t.Errorf("expected permission_mode auto, got %q", c.ClaudePermissionMode)
	}
	if len(c.ClaudeAllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(c.ClaudeAllowedTools))
	}
	if c.ClaudeTurnTimeoutMS != 1800000 {
		t.Errorf("expected turn_timeout 1800000, got %d", c.ClaudeTurnTimeoutMS)
	}
	if c.ClaudeMaxBudgetUSD != 5.0 {
		t.Errorf("expected max_budget 5.0, got %f", c.ClaudeMaxBudgetUSD)
	}
	if !c.ClaudeDangerouslySkipPermissions {
		t.Error("expected dangerously_skip_permissions=true")
	}
	if c.ClaudeAppendSystemPrompt != "Be careful." {
		t.Errorf("expected system prompt, got %q", c.ClaudeAppendSystemPrompt)
	}
}

func TestLoadFromMap_Empty(t *testing.T) {
	c, err := LoadFromMap(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have all defaults
	if c.PollIntervalMS != 30000 {
		t.Errorf("expected default poll interval")
	}
	if c.MaxConcurrentAgents != 10 {
		t.Errorf("expected default concurrency")
	}
}

func TestLoadFromMap_InvalidHookTimeout(t *testing.T) {
	fm := map[string]any{
		"hooks": map[string]any{
			"timeout_ms": -100,
		},
	}
	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Non-positive should fall back to default
	if c.HookTimeoutMS != 60000 {
		t.Errorf("expected default hook timeout, got %d", c.HookTimeoutMS)
	}
}

func TestLoadFromMap_StringIntegers(t *testing.T) {
	fm := map[string]any{
		"polling": map[string]any{
			"interval_ms": "10000",
		},
		"agent": map[string]any{
			"max_concurrent_agents": "3",
		},
	}
	c, err := LoadFromMap(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.PollIntervalMS != 10000 {
		t.Errorf("expected poll=10000, got %d", c.PollIntervalMS)
	}
	if c.MaxConcurrentAgents != 3 {
		t.Errorf("expected max_concurrent=3, got %d", c.MaxConcurrentAgents)
	}
}
