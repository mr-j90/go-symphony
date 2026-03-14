package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jordan/go-symphony/internal/model"
)

var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Manager handles per-issue workspace lifecycle.
type Manager struct {
	root   string
	logger *slog.Logger

	hookAfterCreate  string
	hookBeforeRun    string
	hookAfterRun     string
	hookBeforeRemove string
	hookTimeout      time.Duration
}

// NewManager creates a workspace manager for the given root directory.
func NewManager(root string, logger *slog.Logger) *Manager {
	return &Manager{
		root:        root,
		logger:      logger,
		hookTimeout: 60 * time.Second,
	}
}

// SetHooks updates the hook scripts and timeout.
func (m *Manager) SetHooks(afterCreate, beforeRun, afterRun, beforeRemove string, timeoutMS int) {
	m.hookAfterCreate = afterCreate
	m.hookBeforeRun = beforeRun
	m.hookAfterRun = afterRun
	m.hookBeforeRemove = beforeRemove
	if timeoutMS > 0 {
		m.hookTimeout = time.Duration(timeoutMS) * time.Millisecond
	}
}

// SetRoot updates the workspace root directory.
func (m *Manager) SetRoot(root string) {
	m.root = root
}

// SanitizeKey produces a safe directory name from an issue identifier.
func SanitizeKey(identifier string) string {
	return sanitizeRe.ReplaceAllString(identifier, "_")
}

// CreateForIssue ensures a workspace directory exists for the given issue.
func (m *Manager) CreateForIssue(ctx context.Context, identifier string) (*model.Workspace, error) {
	key := SanitizeKey(identifier)
	wsPath := filepath.Join(m.root, key)

	// Validate path is under workspace root
	absRoot, err := filepath.Abs(m.root)
	if err != nil {
		return nil, fmt.Errorf("workspace root abs path: %w", err)
	}
	absWS, err := filepath.Abs(wsPath)
	if err != nil {
		return nil, fmt.Errorf("workspace abs path: %w", err)
	}
	if !strings.HasPrefix(absWS, absRoot+string(os.PathSeparator)) && absWS != absRoot {
		return nil, fmt.Errorf("workspace path %s is outside root %s", absWS, absRoot)
	}

	// Check if it exists
	info, err := os.Stat(wsPath)
	createdNow := false

	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("workspace stat: %w", err)
		}
		// Create the directory
		if err := os.MkdirAll(wsPath, 0o750); err != nil {
			return nil, fmt.Errorf("workspace mkdir: %w", err)
		}
		createdNow = true
	} else if !info.IsDir() {
		// Exists but not a directory — remove and recreate
		if err := os.Remove(wsPath); err != nil {
			return nil, fmt.Errorf("workspace remove non-dir: %w", err)
		}
		if err := os.MkdirAll(wsPath, 0o750); err != nil {
			return nil, fmt.Errorf("workspace mkdir: %w", err)
		}
		createdNow = true
	}

	ws := &model.Workspace{
		Path:         absWS,
		WorkspaceKey: key,
		CreatedNow:   createdNow,
	}

	// Run after_create hook if newly created
	if createdNow && m.hookAfterCreate != "" {
		m.logger.Info("running after_create hook", "workspace", key)
		if err := m.runHook(ctx, m.hookAfterCreate, absWS); err != nil {
			// Fatal to workspace creation: remove the partially prepared directory
			os.RemoveAll(absWS)
			return nil, fmt.Errorf("after_create hook failed: %w", err)
		}
	}

	return ws, nil
}

// RunBeforeRunHook runs the before_run hook. Failure aborts the attempt.
func (m *Manager) RunBeforeRunHook(ctx context.Context, wsPath string) error {
	if m.hookBeforeRun == "" {
		return nil
	}
	m.logger.Info("running before_run hook", "workspace", wsPath)
	return m.runHook(ctx, m.hookBeforeRun, wsPath)
}

// RunAfterRunHook runs the after_run hook. Failure is logged and ignored.
func (m *Manager) RunAfterRunHook(ctx context.Context, wsPath string) {
	if m.hookAfterRun == "" {
		return
	}
	m.logger.Info("running after_run hook", "workspace", wsPath)
	if err := m.runHook(ctx, m.hookAfterRun, wsPath); err != nil {
		m.logger.Warn("after_run hook failed (ignored)", "error", err, "workspace", wsPath)
	}
}

// CleanWorkspace removes a workspace directory, running before_remove hook first.
func (m *Manager) CleanWorkspace(ctx context.Context, identifier string) {
	key := SanitizeKey(identifier)
	wsPath := filepath.Join(m.root, key)

	absWS, err := filepath.Abs(wsPath)
	if err != nil {
		m.logger.Warn("clean workspace: abs path error", "error", err)
		return
	}

	if _, err := os.Stat(absWS); os.IsNotExist(err) {
		return
	}

	if m.hookBeforeRemove != "" {
		m.logger.Info("running before_remove hook", "workspace", key)
		if err := m.runHook(ctx, m.hookBeforeRemove, absWS); err != nil {
			m.logger.Warn("before_remove hook failed (ignored)", "error", err, "workspace", key)
		}
	}

	if err := os.RemoveAll(absWS); err != nil {
		m.logger.Warn("clean workspace: remove failed", "error", err, "workspace", key)
	} else {
		m.logger.Info("workspace cleaned", "workspace", key)
	}
}

// WorkspacePath returns the absolute path for an issue workspace.
func (m *Manager) WorkspacePath(identifier string) string {
	key := SanitizeKey(identifier)
	p := filepath.Join(m.root, key)
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func (m *Manager) runHook(ctx context.Context, script, cwd string) error {
	hookCtx, cancel := context.WithTimeout(ctx, m.hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "bash", "-lc", script) //nolint:gosec // hook scripts are from trusted config
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("hook timed out after %v", m.hookTimeout)
		}
		return err
	}
	return nil
}
