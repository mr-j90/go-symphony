package workspace

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ABC-123", "ABC-123"},
		{"MT-649", "MT-649"},
		{"hello world", "hello_world"},
		{"a/b/c", "a_b_c"},
		{"special!@#chars", "special___chars"},
		{"dots.and-dashes", "dots.and-dashes"},
		{"under_score", "under_score"},
	}

	for _, tt := range tests {
		result := SanitizeKey(tt.input)
		if result != tt.expected {
			t.Errorf("SanitizeKey(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestCreateForIssue_NewWorkspace(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	ws, err := mgr.CreateForIssue(context.Background(), "MT-100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ws.CreatedNow {
		t.Error("expected CreatedNow=true for new workspace")
	}
	if ws.WorkspaceKey != "MT-100" {
		t.Errorf("expected key=MT-100, got %q", ws.WorkspaceKey)
	}

	// Directory should exist
	info, err := os.Stat(ws.Path)
	if err != nil {
		t.Fatalf("workspace dir not found: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected workspace to be a directory")
	}
}

func TestCreateForIssue_ExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Create first
	_, err := mgr.CreateForIssue(context.Background(), "MT-100")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Reuse
	ws, err := mgr.CreateForIssue(context.Background(), "MT-100")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if ws.CreatedNow {
		t.Error("expected CreatedNow=false for reused workspace")
	}
}

func TestCreateForIssue_PathContainment(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// This should not escape root since we sanitize
	ws, err := mgr.CreateForIssue(context.Background(), "../../etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sanitized key should be safe
	if ws.WorkspaceKey != "_.._..___etc_passwd" {
		t.Logf("sanitized key: %s", ws.WorkspaceKey)
	}

	// Path must be under root
	absRoot, _ := filepath.Abs(root)
	if !filepath.HasPrefix(ws.Path, absRoot) {
		t.Errorf("workspace path %s is not under root %s", ws.Path, absRoot)
	}
}

func TestCleanWorkspace(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Create workspace
	ws, err := mgr.CreateForIssue(context.Background(), "MT-100")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify exists
	if _, err := os.Stat(ws.Path); err != nil {
		t.Fatalf("workspace should exist: %v", err)
	}

	// Clean
	mgr.CleanWorkspace(context.Background(), "MT-100")

	// Verify removed
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Error("workspace should be removed after clean")
	}
}

func TestCleanWorkspace_NonExistent(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Should not panic or error
	mgr.CleanWorkspace(context.Background(), "NONEXISTENT-123")
}

func TestCreateForIssue_AfterCreateHook(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Set a hook that creates a marker file
	mgr.SetHooks("touch .hook-ran", "", "", "", 10000)

	ws, err := mgr.CreateForIssue(context.Background(), "MT-200")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	markerPath := filepath.Join(ws.Path, ".hook-ran")
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("after_create hook should have created .hook-ran: %v", err)
	}
}

func TestCreateForIssue_AfterCreateHookFailure(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := NewManager(root, logger)

	// Set a hook that fails
	mgr.SetHooks("exit 1", "", "", "", 10000)

	_, err := mgr.CreateForIssue(context.Background(), "MT-300")
	if err == nil {
		t.Fatal("expected error when after_create hook fails")
	}

	// Workspace should be cleaned up
	wsPath := filepath.Join(root, "MT-300")
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Error("workspace should be removed after hook failure")
	}
}

func TestWorkspacePath(t *testing.T) {
	mgr := NewManager("/tmp/ws", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	path := mgr.WorkspacePath("MT-100")
	if path != "/tmp/ws/MT-100" {
		t.Errorf("expected /tmp/ws/MT-100, got %q", path)
	}
}
