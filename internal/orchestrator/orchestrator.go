package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jordan/go-symphony/internal/agent"
	"github.com/jordan/go-symphony/internal/config"
	"github.com/jordan/go-symphony/internal/linear"
	"github.com/jordan/go-symphony/internal/model"
	"github.com/jordan/go-symphony/internal/workflow"
	"github.com/jordan/go-symphony/internal/workspace"
)

// Orchestrator owns the poll tick and all scheduling state.
type Orchestrator struct {
	mu sync.Mutex

	cfg          *config.Config
	wfDef        *model.WorkflowDefinition
	linear       *linear.Client
	ws           *workspace.Manager
	runner       *agent.Runner
	claudeRunner *agent.ClaudeRunner
	logger       *slog.Logger

	// Runtime state (single authority)
	running       map[string]*model.RunningEntry // issue_id -> entry
	claimed       map[string]bool
	retryAttempts map[string]*model.RetryEntry
	completed     map[string]bool
	codexTotals   model.CodexTotals
	rateLimits    *model.RateLimitInfo

	// For observer notification
	onStateChange func()
}

// New creates a new orchestrator.
func New(
	cfg *config.Config,
	wfDef *model.WorkflowDefinition,
	linearClient *linear.Client,
	ws *workspace.Manager,
	runner *agent.Runner,
	logger *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		cfg:           cfg,
		wfDef:         wfDef,
		linear:        linearClient,
		ws:            ws,
		runner:        runner,
		logger:        logger,
		running:       make(map[string]*model.RunningEntry),
		claimed:       make(map[string]bool),
		retryAttempts: make(map[string]*model.RetryEntry),
		completed:     make(map[string]bool),
	}
}

// SetClaudeRunner sets the Claude Code runner for claude_code agent type.
func (o *Orchestrator) SetClaudeRunner(r *agent.ClaudeRunner) {
	o.claudeRunner = r
}

// SetOnStateChange registers a callback for state changes (used by status surface).
func (o *Orchestrator) SetOnStateChange(fn func()) {
	o.onStateChange = fn
}

// ReloadWorkflow updates the workflow definition and reconfigures dependent components.
func (o *Orchestrator) ReloadWorkflow(wfDef *model.WorkflowDefinition, cfg *config.Config) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.wfDef = wfDef
	o.cfg = cfg

	// Update workspace manager
	o.ws.SetRoot(cfg.WorkspaceRoot)
	o.ws.SetHooks(cfg.HookAfterCreate, cfg.HookBeforeRun, cfg.HookAfterRun, cfg.HookBeforeRemove, cfg.HookTimeoutMS)

	// Update agent runner configs
	if o.runner != nil {
		o.runner.UpdateConfig(agent.RunnerConfig{
			Command:           cfg.CodexCommand,
			ApprovalPolicy:    cfg.CodexApprovalPolicy,
			ThreadSandbox:     cfg.CodexThreadSandbox,
			TurnSandboxPolicy: cfg.CodexTurnSandboxPolicy,
			TurnTimeoutMS:     cfg.CodexTurnTimeoutMS,
			ReadTimeoutMS:     cfg.CodexReadTimeoutMS,
		})
	}
	if o.claudeRunner != nil {
		o.claudeRunner.UpdateClaudeConfig(agent.ClaudeRunnerConfig{
			Command:                    cfg.ClaudeCommand,
			Model:                      cfg.ClaudeModel,
			MaxTurns:                   cfg.ClaudeMaxTurns,
			AllowedTools:               cfg.ClaudeAllowedTools,
			DisallowedTools:            cfg.ClaudeDisallowedTools,
			PermissionMode:             cfg.ClaudePermissionMode,
			DangerouslySkipPermissions: cfg.ClaudeDangerouslySkipPermissions,
			AppendSystemPrompt:         cfg.ClaudeAppendSystemPrompt,
			TurnTimeoutMS:              cfg.ClaudeTurnTimeoutMS,
			MaxBudgetUSD:               cfg.ClaudeMaxBudgetUSD,
		})
	}

	// Rebuild Linear client if needed
	o.linear = linear.NewClient(cfg.TrackerEndpoint, cfg.TrackerAPIKey, cfg.TrackerProjectSlug)

	o.logger.Info("workflow reloaded, config updated", "agent_type", cfg.AgentType)
}

// Run starts the orchestrator's poll loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	// Startup validation
	if err := o.cfg.ValidateForDispatch(); err != nil {
		return fmt.Errorf("startup validation failed: %w", err)
	}

	// Startup terminal workspace cleanup
	o.startupCleanup(ctx)

	// Initial tick
	o.tick(ctx)

	// Poll loop
	ticker := time.NewTicker(o.cfg.PollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator shutting down")
			o.cancelAllRunning()
			return nil
		case <-ticker.C:
			// Re-read poll interval in case it changed
			ticker.Reset(o.cfg.PollInterval())
			o.tick(ctx)
		}
	}
}

// TriggerPoll triggers an immediate poll+reconcile cycle.
func (o *Orchestrator) TriggerPoll(ctx context.Context) {
	go o.tick(ctx)
}

func (o *Orchestrator) tick(ctx context.Context) {
	o.logger.Debug("tick start")

	// Reconcile first
	o.reconcile(ctx)

	// Dispatch preflight validation
	if err := o.cfg.ValidateForDispatch(); err != nil {
		o.logger.Error("dispatch validation failed, skipping dispatch", "error", err)
		o.notifyObservers()
		return
	}

	// Fetch candidates
	issues, err := o.linear.FetchCandidateIssues(ctx, o.cfg.TrackerActiveStates)
	if err != nil {
		o.logger.Error("failed to fetch candidate issues", "error", err)
		o.notifyObservers()
		return
	}

	// Sort for dispatch
	sortForDispatch(issues)

	// Dispatch eligible
	for _, issue := range issues {
		o.mu.Lock()
		slots := o.availableSlots()
		o.mu.Unlock()

		if slots <= 0 {
			break
		}

		if o.shouldDispatch(issue) {
			o.dispatchIssue(ctx, issue, nil)
		}
	}

	o.notifyObservers()
}

func (o *Orchestrator) shouldDispatch(issue model.Issue) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Required fields
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}

	// Must be in active states
	if !o.cfg.IsActiveState(issue.State) {
		return false
	}

	// Must not be in terminal states
	if o.cfg.IsTerminalState(issue.State) {
		return false
	}

	// Not already running or claimed
	if _, ok := o.running[issue.ID]; ok {
		return false
	}
	if o.claimed[issue.ID] {
		return false
	}

	// Global concurrency
	if o.availableSlots() <= 0 {
		return false
	}

	// Per-state concurrency
	if !o.perStateSlotAvailable(issue.State) {
		return false
	}

	// Blocker rule for Todo state
	if strings.EqualFold(issue.State, "todo") {
		for _, b := range issue.BlockedBy {
			if b.State != nil && !o.cfg.IsTerminalState(*b.State) {
				return false
			}
		}
	}

	return true
}

func (o *Orchestrator) availableSlots() int {
	running := len(o.running)
	max := o.cfg.MaxConcurrentAgents
	if max <= running {
		return 0
	}
	return max - running
}

func (o *Orchestrator) perStateSlotAvailable(state string) bool {
	lower := strings.ToLower(state)
	limit, ok := o.cfg.MaxConcurrentByState[lower]
	if !ok {
		return true // No per-state limit
	}

	count := 0
	for _, entry := range o.running {
		if strings.EqualFold(entry.Issue.State, state) {
			count++
		}
	}
	return count < limit
}

func (o *Orchestrator) dispatchIssue(ctx context.Context, issue model.Issue, attempt *int) {
	o.mu.Lock()
	o.claimed[issue.ID] = true
	delete(o.retryAttempts, issue.ID)

	entry := &model.RunningEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Issue:      issue,
		StartedAt:  time.Now().UTC(),
	}
	if attempt != nil {
		entry.RetryAttempt = *attempt
	}

	workerCtx, cancel := context.WithCancel(ctx)
	entry.CancelFunc = cancel
	o.running[issue.ID] = entry
	o.mu.Unlock()

	o.logger.Info("dispatching issue",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"state", issue.State,
		"attempt", attempt,
	)

	// Transition issue state on dispatch (e.g., Todo -> In Progress)
	if o.cfg.DispatchTransitionState != "" && !strings.EqualFold(issue.State, o.cfg.DispatchTransitionState) {
		go func() {
			if err := o.linear.TransitionIssueState(ctx, issue.ID, o.cfg.DispatchTransitionState); err != nil {
				o.logger.Warn("failed to transition issue state on dispatch",
					"issue_id", issue.ID,
					"issue_identifier", issue.Identifier,
					"target_state", o.cfg.DispatchTransitionState,
					"error", err,
				)
			} else {
				o.logger.Info("issue state transitioned",
					"issue_id", issue.ID,
					"issue_identifier", issue.Identifier,
					"from", issue.State,
					"to", o.cfg.DispatchTransitionState,
				)
			}
		}()
	}

	// Spawn worker goroutine
	go o.runWorker(workerCtx, issue, attempt, entry)
}

func (o *Orchestrator) runWorker(ctx context.Context, issue model.Issue, attempt *int, entry *model.RunningEntry) {
	o.mu.Lock()
	agentType := o.cfg.AgentType
	o.mu.Unlock()

	switch agentType {
	case "claude_code":
		o.runWorkerClaude(ctx, issue, attempt, entry)
	default:
		o.runWorkerCodex(ctx, issue, attempt, entry)
	}
}

func (o *Orchestrator) runWorkerCodex(ctx context.Context, issue model.Issue, attempt *int, entry *model.RunningEntry) {
	var workerErr error
	defer func() {
		o.onWorkerExit(issue.ID, entry, workerErr)
	}()

	// Create workspace
	ws, err := o.ws.CreateForIssue(ctx, issue.Identifier)
	if err != nil {
		workerErr = fmt.Errorf("workspace error: %w", err)
		return
	}

	// Before run hook
	if err := o.ws.RunBeforeRunHook(ctx, ws.Path); err != nil {
		workerErr = fmt.Errorf("before_run hook error: %w", err)
		o.ws.RunAfterRunHook(ctx, ws.Path)
		return
	}

	// Start agent session
	session, err := o.runner.StartSession(ctx, ws.Path)
	if err != nil {
		workerErr = fmt.Errorf("agent session startup error: %w", err)
		o.ws.RunAfterRunHook(ctx, ws.Path)
		return
	}
	defer func() {
		session.Stop()
		o.ws.RunAfterRunHook(ctx, ws.Path)
	}()

	maxTurns := o.cfg.MaxTurns
	currentIssue := issue

	for turnNum := 1; turnNum <= maxTurns; turnNum++ {
		// Build prompt
		prompt, err := o.buildTurnPrompt(currentIssue, attempt, turnNum, maxTurns)
		if err != nil {
			workerErr = fmt.Errorf("prompt error: %w", err)
			return
		}

		// Run turn
		err = session.RunTurn(ctx, prompt, currentIssue, turnNum, func(event model.CodexEvent) {
			o.onCodexEvent(issue.ID, event)
		})
		if err != nil {
			workerErr = fmt.Errorf("agent turn error: %w", err)
			return
		}

		// Check issue state
		refreshed, err := o.linear.FetchIssueStatesByIDs(ctx, []string{issue.ID})
		if err != nil {
			workerErr = fmt.Errorf("issue state refresh error: %w", err)
			return
		}
		if len(refreshed) > 0 {
			currentIssue = refreshed[0]
		}

		if !o.cfg.IsActiveState(currentIssue.State) {
			break
		}
	}

	// Normal exit
}

func (o *Orchestrator) runWorkerClaude(ctx context.Context, issue model.Issue, attempt *int, entry *model.RunningEntry) {
	var workerErr error
	defer func() {
		o.onWorkerExit(issue.ID, entry, workerErr)
	}()

	if o.claudeRunner == nil {
		workerErr = fmt.Errorf("claude_code agent type configured but no claude runner set")
		return
	}

	// Create workspace
	ws, err := o.ws.CreateForIssue(ctx, issue.Identifier)
	if err != nil {
		workerErr = fmt.Errorf("workspace error: %w", err)
		return
	}

	// Before run hook
	if err := o.ws.RunBeforeRunHook(ctx, ws.Path); err != nil {
		workerErr = fmt.Errorf("before_run hook error: %w", err)
		o.ws.RunAfterRunHook(ctx, ws.Path)
		return
	}

	defer func() {
		o.ws.RunAfterRunHook(ctx, ws.Path)
	}()

	// Start Claude session
	session, err := o.claudeRunner.StartClaudeSession(ctx, ws.Path)
	if err != nil {
		workerErr = fmt.Errorf("agent session startup error: %w", err)
		return
	}
	defer session.Stop()

	maxTurns := o.cfg.MaxTurns
	currentIssue := issue

	for turnNum := 1; turnNum <= maxTurns; turnNum++ {
		// Build prompt
		prompt, err := o.buildTurnPrompt(currentIssue, attempt, turnNum, maxTurns)
		if err != nil {
			workerErr = fmt.Errorf("prompt error: %w", err)
			return
		}

		// Run turn via Claude Code
		err = session.RunTurn(ctx, prompt, currentIssue, turnNum, ws.Path, func(event model.CodexEvent) {
			o.onCodexEvent(issue.ID, event)
		})
		if err != nil {
			workerErr = fmt.Errorf("agent turn error: %w", err)
			return
		}

		// Check issue state
		refreshed, err := o.linear.FetchIssueStatesByIDs(ctx, []string{issue.ID})
		if err != nil {
			workerErr = fmt.Errorf("issue state refresh error: %w", err)
			return
		}
		if len(refreshed) > 0 {
			currentIssue = refreshed[0]
		}

		if !o.cfg.IsActiveState(currentIssue.State) {
			break
		}
	}

	// Normal exit
}

func (o *Orchestrator) buildTurnPrompt(issue model.Issue, attempt *int, turnNum, maxTurns int) (string, error) {
	o.mu.Lock()
	tmpl := o.wfDef.PromptTemplate
	o.mu.Unlock()

	if turnNum == 1 {
		return workflow.RenderPrompt(tmpl, issue, attempt)
	}

	// Continuation turn - send guidance, not the full original prompt
	guidance := fmt.Sprintf(
		"Continue working on %s: %s. This is turn %d of %d. "+
			"The issue is still in state '%s'. Please continue where you left off.",
		issue.Identifier, issue.Title, turnNum, maxTurns, issue.State,
	)
	return guidance, nil
}

func (o *Orchestrator) onCodexEvent(issueID string, event model.CodexEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()

	entry, ok := o.running[issueID]
	if !ok {
		return
	}

	entry.Session.LastCodexEvent = event.Event
	entry.Session.LastCodexTimestamp = &event.Timestamp
	entry.Session.LastCodexMessage = event.Message
	entry.Session.CodexAppServerPID = event.CodexAppServerPID

	if event.SessionID != "" {
		entry.Session.SessionID = event.SessionID
		entry.Session.ThreadID = event.ThreadID
		entry.Session.TurnID = event.TurnID
	}

	// Update token counts (prefer absolute totals)
	if event.Usage != nil {
		u := event.Usage
		if u.TotalTokens > 0 {
			// Compute delta from last reported
			inputDelta := u.InputTokens - entry.Session.LastReportedInputToks
			outputDelta := u.OutputTokens - entry.Session.LastReportedOutputToks

			if inputDelta > 0 {
				entry.Session.CodexInputTokens += inputDelta
				o.codexTotals.InputTokens += inputDelta
			}
			if outputDelta > 0 {
				entry.Session.CodexOutputTokens += outputDelta
				o.codexTotals.OutputTokens += outputDelta
			}

			totalDelta := u.TotalTokens - entry.Session.LastReportedTotalToks
			if totalDelta > 0 {
				entry.Session.CodexTotalTokens += totalDelta
				o.codexTotals.TotalTokens += totalDelta
			}

			entry.Session.LastReportedInputToks = u.InputTokens
			entry.Session.LastReportedOutputToks = u.OutputTokens
			entry.Session.LastReportedTotalToks = u.TotalTokens
		}
	}

	if event.Event == "turn_completed" || event.Event == "session_started" {
		entry.Session.TurnCount++
	}
}

func (o *Orchestrator) onWorkerExit(issueID string, entry *model.RunningEntry, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.running, issueID)

	// Add runtime seconds
	elapsed := time.Since(entry.StartedAt).Seconds()
	o.codexTotals.SecondsRunning += elapsed

	if err == nil {
		// Normal exit - schedule continuation retry
		o.completed[issueID] = true
		o.scheduleRetry(issueID, entry.Identifier, 1, "continuation", 1000)

		o.logger.Info("worker completed normally",
			"issue_id", issueID,
			"issue_identifier", entry.Identifier,
			"runtime_s", fmt.Sprintf("%.1f", elapsed),
		)
	} else {
		// Abnormal exit - exponential backoff retry
		nextAttempt := entry.RetryAttempt + 1
		delay := o.calculateBackoff(nextAttempt)

		o.scheduleRetry(issueID, entry.Identifier, nextAttempt, err.Error(), delay)

		o.logger.Warn("worker failed",
			"issue_id", issueID,
			"issue_identifier", entry.Identifier,
			"error", err,
			"next_attempt", nextAttempt,
			"retry_delay_ms", delay,
		)
	}

	o.notifyObserversLocked()
}

func (o *Orchestrator) calculateBackoff(attempt int) int64 {
	delay := int64(10000 * math.Pow(2, float64(attempt-1)))
	max := int64(o.cfg.MaxRetryBackoffMS)
	if delay > max {
		delay = max
	}
	return delay
}

func (o *Orchestrator) scheduleRetry(issueID, identifier string, attempt int, errMsg string, delayMS int64) {
	// Cancel existing retry timer
	delete(o.retryAttempts, issueID)

	dueAt := time.Now().UnixMilli() + delayMS
	entry := &model.RetryEntry{
		IssueID:    issueID,
		Identifier: identifier,
		Attempt:    attempt,
		DueAtMS:    dueAt,
		Error:      errMsg,
	}
	o.retryAttempts[issueID] = entry

	// Schedule timer
	go func() {
		time.Sleep(time.Duration(delayMS) * time.Millisecond)
		o.onRetryTimer(issueID)
	}()
}

func (o *Orchestrator) onRetryTimer(issueID string) {
	o.mu.Lock()
	entry, ok := o.retryAttempts[issueID]
	if !ok {
		o.mu.Unlock()
		return
	}
	delete(o.retryAttempts, issueID)
	o.mu.Unlock()

	// Fetch current candidates
	ctx := context.Background()
	issues, err := o.linear.FetchCandidateIssues(ctx, o.cfg.TrackerActiveStates)
	if err != nil {
		o.mu.Lock()
		o.scheduleRetry(issueID, entry.Identifier, entry.Attempt+1, "retry poll failed", o.calculateBackoff(entry.Attempt+1))
		o.mu.Unlock()
		return
	}

	// Find the issue
	var found *model.Issue
	for i := range issues {
		if issues[i].ID == issueID {
			found = &issues[i]
			break
		}
	}

	if found == nil {
		// Release claim
		o.mu.Lock()
		delete(o.claimed, issueID)
		o.mu.Unlock()
		o.logger.Info("retry: issue no longer candidate, released",
			"issue_id", issueID,
			"issue_identifier", entry.Identifier,
		)
		return
	}

	o.mu.Lock()
	slots := o.availableSlots()
	o.mu.Unlock()

	if slots <= 0 {
		o.mu.Lock()
		o.scheduleRetry(issueID, entry.Identifier, entry.Attempt+1, "no available orchestrator slots", o.calculateBackoff(entry.Attempt+1))
		o.mu.Unlock()
		return
	}

	attempt := entry.Attempt
	o.dispatchIssue(ctx, *found, &attempt)
}

func (o *Orchestrator) reconcile(ctx context.Context) {
	o.mu.Lock()
	// Stall detection
	stallTimeout := time.Duration(o.cfg.CodexStallTimeoutMS) * time.Millisecond
	if stallTimeout > 0 {
		for id, entry := range o.running {
			var lastActivity time.Time
			if entry.Session.LastCodexTimestamp != nil {
				lastActivity = *entry.Session.LastCodexTimestamp
			} else {
				lastActivity = entry.StartedAt
			}

			if time.Since(lastActivity) > stallTimeout {
				o.logger.Warn("stall detected, terminating",
					"issue_id", id,
					"issue_identifier", entry.Identifier,
					"stall_duration", time.Since(lastActivity),
				)
				entry.CancelFunc()
			}
		}
	}

	// Collect running IDs
	ids := make([]string, 0, len(o.running))
	for id := range o.running {
		ids = append(ids, id)
	}
	o.mu.Unlock()

	if len(ids) == 0 {
		return
	}

	// Fetch current states
	refreshed, err := o.linear.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		o.logger.Debug("reconciliation state refresh failed, keeping workers", "error", err)
		return
	}

	refreshMap := make(map[string]model.Issue, len(refreshed))
	for _, issue := range refreshed {
		refreshMap[issue.ID] = issue
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	for id, entry := range o.running {
		issue, ok := refreshMap[id]
		if !ok {
			continue
		}

		if o.cfg.IsTerminalState(issue.State) {
			o.logger.Info("issue now terminal, stopping worker and cleaning workspace",
				"issue_id", id,
				"issue_identifier", entry.Identifier,
				"new_state", issue.State,
			)
			entry.CancelFunc()
			go o.ws.CleanWorkspace(context.Background(), entry.Identifier)
			delete(o.running, id)
			delete(o.claimed, id)
		} else if o.cfg.IsActiveState(issue.State) {
			entry.Issue = issue
		} else {
			// Not active, not terminal: stop without cleanup
			o.logger.Info("issue no longer active, stopping worker",
				"issue_id", id,
				"issue_identifier", entry.Identifier,
				"new_state", issue.State,
			)
			entry.CancelFunc()
			delete(o.running, id)
			delete(o.claimed, id)
		}
	}
}

func (o *Orchestrator) startupCleanup(ctx context.Context) {
	o.logger.Info("performing startup terminal workspace cleanup")

	issues, err := o.linear.FetchIssuesByStates(ctx, o.cfg.TrackerTerminalStates)
	if err != nil {
		o.logger.Warn("startup cleanup: failed to fetch terminal issues", "error", err)
		return
	}

	for _, issue := range issues {
		o.ws.CleanWorkspace(ctx, issue.Identifier)
	}
	o.logger.Info("startup cleanup complete", "terminal_issues", len(issues))
}

func (o *Orchestrator) cancelAllRunning() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.running {
		entry.CancelFunc()
	}
}

func (o *Orchestrator) notifyObservers() {
	if o.onStateChange != nil {
		o.onStateChange()
	}
}

func (o *Orchestrator) notifyObserversLocked() {
	if o.onStateChange != nil {
		go o.onStateChange()
	}
}

// Snapshot returns a snapshot of the current orchestrator state for the API/status surface.
func (o *Orchestrator) Snapshot() StateSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()

	snap := StateSnapshot{
		GeneratedAt: time.Now().UTC(),
		Running:     make([]RunningSnapshot, 0, len(o.running)),
		Retrying:    make([]RetrySnapshot, 0, len(o.retryAttempts)),
		CodexTotals: o.codexTotals,
		RateLimits:  o.rateLimits,
	}

	// Add active session runtime to totals
	for _, entry := range o.running {
		elapsed := time.Since(entry.StartedAt).Seconds()
		snap.CodexTotals.SecondsRunning += elapsed

		snap.Running = append(snap.Running, RunningSnapshot{
			IssueID:         entry.IssueID,
			IssueIdentifier: entry.Identifier,
			State:           entry.Issue.State,
			SessionID:       entry.Session.SessionID,
			TurnCount:       entry.Session.TurnCount,
			LastEvent:       entry.Session.LastCodexEvent,
			LastMessage:     entry.Session.LastCodexMessage,
			StartedAt:       entry.StartedAt,
			LastEventAt:     entry.Session.LastCodexTimestamp,
			Tokens: TokenSnapshot{
				InputTokens:  entry.Session.CodexInputTokens,
				OutputTokens: entry.Session.CodexOutputTokens,
				TotalTokens:  entry.Session.CodexTotalTokens,
			},
		})
	}

	for _, entry := range o.retryAttempts {
		snap.Retrying = append(snap.Retrying, RetrySnapshot{
			IssueID:         entry.IssueID,
			IssueIdentifier: entry.Identifier,
			Attempt:         entry.Attempt,
			DueAt:           time.UnixMilli(entry.DueAtMS).UTC(),
			Error:           entry.Error,
		})
	}

	return snap
}

// StateSnapshot is the serializable state for the API.
type StateSnapshot struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Running     []RunningSnapshot    `json:"running"`
	Retrying    []RetrySnapshot      `json:"retrying"`
	CodexTotals model.CodexTotals    `json:"codex_totals"`
	RateLimits  *model.RateLimitInfo `json:"rate_limits"`
}

type RunningSnapshot struct {
	IssueID         string        `json:"issue_id"`
	IssueIdentifier string        `json:"issue_identifier"`
	State           string        `json:"state"`
	SessionID       string        `json:"session_id"`
	TurnCount       int           `json:"turn_count"`
	LastEvent       string        `json:"last_event"`
	LastMessage     string        `json:"last_message"`
	StartedAt       time.Time     `json:"started_at"`
	LastEventAt     *time.Time    `json:"last_event_at"`
	Tokens          TokenSnapshot `json:"tokens"`
}

type TokenSnapshot struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type RetrySnapshot struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         int       `json:"attempt"`
	DueAt           time.Time `json:"due_at"`
	Error           string    `json:"error"`
}

// sortForDispatch sorts issues by priority (ascending, nil last), then created_at (oldest first),
// then identifier (lexicographic).
func sortForDispatch(issues []model.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]

		// Priority: lower is higher priority, nil sorts last
		aPri := priorityVal(a.Priority)
		bPri := priorityVal(b.Priority)
		if aPri != bPri {
			return aPri < bPri
		}

		// Created at: oldest first
		aTime := timeVal(a.CreatedAt)
		bTime := timeVal(b.CreatedAt)
		if !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}

		// Identifier: lexicographic
		return a.Identifier < b.Identifier
	})
}

func priorityVal(p *int) int {
	if p == nil {
		return math.MaxInt32
	}
	return *p
}

func timeVal(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
