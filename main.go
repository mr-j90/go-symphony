package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jordan/go-symphony/internal/agent"
	"github.com/jordan/go-symphony/internal/config"
	"github.com/jordan/go-symphony/internal/linear"
	"github.com/jordan/go-symphony/internal/model"
	"github.com/jordan/go-symphony/internal/orchestrator"
	"github.com/jordan/go-symphony/internal/server"
	"github.com/jordan/go-symphony/internal/workflow"
	"github.com/jordan/go-symphony/internal/workspace"
)

func main() {
	port := flag.Int("port", -1, "HTTP server port (overrides server.port in WORKFLOW.md)")
	flag.Parse()

	// Workflow path: positional arg or default
	workflowPath := "WORKFLOW.md"
	if flag.NArg() > 0 {
		workflowPath = flag.Arg(0)
	}

	// Setup structured logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Load workflow
	wfDef, err := workflow.Load(workflowPath)
	if err != nil {
		logger.Error("failed to load workflow", "error", err, "path", workflowPath)
		os.Exit(1)
	}
	logger.Info("workflow loaded", "path", workflowPath)

	// Build config from front matter
	cfg, err := config.LoadFromMap(wfDef.Config)
	if err != nil {
		logger.Error("failed to parse config", "error", err)
		os.Exit(1)
	}

	// Validate
	if err := cfg.ValidateForDispatch(); err != nil {
		logger.Error("config validation failed", "error", err)
		os.Exit(1)
	}

	// Create Linear client
	linearClient := linear.NewClient(cfg.TrackerEndpoint, cfg.TrackerAPIKey, cfg.TrackerProjectSlug)

	// Create workspace manager
	wsMgr := workspace.NewManager(cfg.WorkspaceRoot, logger)
	wsMgr.SetHooks(cfg.HookAfterCreate, cfg.HookBeforeRun, cfg.HookAfterRun, cfg.HookBeforeRemove, cfg.HookTimeoutMS)

	// Create agent runners based on configured type
	var codexRunner *agent.Runner
	var claudeRunner *agent.ClaudeRunner

	switch cfg.AgentType {
	case "claude_code":
		claudeRunner = agent.NewClaudeRunner(agent.ClaudeRunnerConfig{
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
		}, logger)
		logger.Info("using Claude Code agent runner",
			"command", cfg.ClaudeCommand,
			"model", cfg.ClaudeModel,
			"permission_mode", cfg.ClaudePermissionMode,
		)
	default: // "codex"
		codexRunner = agent.NewRunner(agent.RunnerConfig{
			Command:           cfg.CodexCommand,
			ApprovalPolicy:    cfg.CodexApprovalPolicy,
			ThreadSandbox:     cfg.CodexThreadSandbox,
			TurnSandboxPolicy: cfg.CodexTurnSandboxPolicy,
			TurnTimeoutMS:     cfg.CodexTurnTimeoutMS,
			ReadTimeoutMS:     cfg.CodexReadTimeoutMS,
		}, logger)
		logger.Info("using Codex agent runner", "command", cfg.CodexCommand)
	}

	// Create orchestrator
	orch := orchestrator.New(cfg, wfDef, linearClient, wsMgr, codexRunner, logger)
	if claudeRunner != nil {
		orch.SetClaudeRunner(claudeRunner)
	}

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Start workflow watcher for dynamic reload
	if err := workflow.Watch(ctx, workflowPath, func(newWfDef *model.WorkflowDefinition) {
		newCfg, err := config.LoadFromMap(newWfDef.Config)
		if err != nil {
			logger.Error("workflow reload: config parse failed, keeping last good config", "error", err)
			return
		}
		orch.ReloadWorkflow(newWfDef, newCfg)
	}, logger); err != nil {
		logger.Warn("failed to start workflow watcher", "error", err)
	}

	// Determine HTTP server port
	httpPort := -1
	if *port >= 0 {
		httpPort = *port
	} else if cfg.ServerPort != nil {
		httpPort = *cfg.ServerPort
	}

	// Start optional HTTP server
	if httpPort >= 0 {
		srv := server.New(orch, httpPort, logger)
		if err := srv.Start(ctx); err != nil {
			logger.Error("failed to start HTTP server", "error", err)
			os.Exit(1)
		}
		logger.Info("HTTP server listening", "addr", srv.Addr())
	}

	// Run orchestrator (blocks until ctx is cancelled)
	logger.Info("symphony starting",
		"agent_type", cfg.AgentType,
		"tracker", cfg.TrackerKind,
		"project", cfg.TrackerProjectSlug,
		"poll_interval_ms", cfg.PollIntervalMS,
		"max_concurrent", cfg.MaxConcurrentAgents,
		"workspace_root", cfg.WorkspaceRoot,
	)

	if err := orch.Run(ctx); err != nil {
		logger.Error("orchestrator error", "error", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "symphony stopped")
}
