package model

import "time"

// Issue is the normalized issue record from the tracker.
type Issue struct {
	ID          string
	Identifier  string // e.g. "ABC-123"
	Title       string
	Description *string
	Priority    *int
	State       string
	BranchName  *string
	URL         *string
	Labels      []string
	BlockedBy   []BlockerRef
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

// BlockerRef references an issue that blocks this one.
type BlockerRef struct {
	ID         *string
	Identifier *string
	State      *string
}

// WorkflowDefinition is the parsed WORKFLOW.md payload.
type WorkflowDefinition struct {
	Config         map[string]any
	PromptTemplate string
}

// Workspace represents a per-issue workspace on the filesystem.
type Workspace struct {
	Path         string
	WorkspaceKey string
	CreatedNow   bool
}

// RunAttempt tracks one execution attempt for an issue.
type RunAttempt struct {
	IssueID         string
	IssueIdentifier string
	Attempt         *int // nil for first run, >=1 for retries
	WorkspacePath   string
	StartedAt       time.Time
	Status          RunStatus
	Error           string
}

// RunStatus is the lifecycle phase of a run attempt.
type RunStatus int

const (
	RunPreparingWorkspace RunStatus = iota
	RunBuildingPrompt
	RunLaunchingAgentProcess
	RunInitializingSession
	RunStreamingTurn
	RunFinishing
	RunSucceeded
	RunFailed
	RunTimedOut
	RunStalled
	RunCanceledByReconciliation
)

// LiveSession holds coding-agent session metadata while running.
type LiveSession struct {
	SessionID              string // "<thread_id>-<turn_id>"
	ThreadID               string
	TurnID                 string
	CodexAppServerPID      string
	LastCodexEvent         string
	LastCodexTimestamp     *time.Time
	LastCodexMessage       string
	CodexInputTokens       int64
	CodexOutputTokens      int64
	CodexTotalTokens       int64
	LastReportedInputToks  int64
	LastReportedOutputToks int64
	LastReportedTotalToks  int64
	TurnCount              int
}

// RetryEntry is the scheduled retry state for an issue.
type RetryEntry struct {
	IssueID    string
	Identifier string
	IssueTitle string
	Attempt    int // 1-based
	DueAtMS    int64
	Error      string
}

// RunningEntry tracks a running worker in the orchestrator.
type RunningEntry struct {
	IssueID      string
	Identifier   string
	Issue        Issue
	Session      LiveSession
	RetryAttempt int
	StartedAt    time.Time
	CancelFunc   func()
}

// CodexTotals holds aggregate token and runtime counters.
type CodexTotals struct {
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	SecondsRunning float64
}

// CodexEvent is emitted from the agent runner to the orchestrator.
type CodexEvent struct {
	Event             string
	Timestamp         time.Time
	CodexAppServerPID string
	Usage             *TokenUsage
	Message           string
	Error             string
	SessionID         string
	ThreadID          string
	TurnID            string
}

// TokenUsage holds token count data from an agent event.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// RateLimitInfo holds the latest rate limit snapshot.
type RateLimitInfo struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// Finding represents an issue discovered by the agent that is unrelated to the current task.
// Agents write these to .symphony/findings.json in the workspace; the orchestrator
// reads them after each turn and opens Linear issues for each one.
type Finding struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
