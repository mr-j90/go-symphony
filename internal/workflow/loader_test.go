package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse_FullFrontMatter(t *testing.T) {
	content := `---
tracker:
  kind: linear
  project_slug: my-project
polling:
  interval_ms: 5000
---
You are working on {{ issue.identifier }}: {{ issue.title }}`

	wf, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tracker, ok := wf.Config["tracker"].(map[string]any)
	if !ok {
		t.Fatal("expected tracker config")
	}
	if tracker["kind"] != "linear" {
		t.Errorf("expected tracker.kind=linear, got %v", tracker["kind"])
	}
	if tracker["project_slug"] != "my-project" {
		t.Errorf("expected project_slug=my-project, got %v", tracker["project_slug"])
	}

	polling, ok := wf.Config["polling"].(map[string]any)
	if !ok {
		t.Fatal("expected polling config")
	}
	if polling["interval_ms"] != 5000 {
		t.Errorf("expected interval_ms=5000, got %v", polling["interval_ms"])
	}

	expected := "You are working on {{ issue.identifier }}: {{ issue.title }}"
	if wf.PromptTemplate != expected {
		t.Errorf("expected prompt %q, got %q", expected, wf.PromptTemplate)
	}
}

func TestParse_NoFrontMatter(t *testing.T) {
	content := "Just a prompt with no config"
	wf, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wf.Config) != 0 {
		t.Errorf("expected empty config, got %v", wf.Config)
	}
	if wf.PromptTemplate != content {
		t.Errorf("expected prompt %q, got %q", content, wf.PromptTemplate)
	}
}

func TestParse_EmptyFrontMatter(t *testing.T) {
	content := `---
---
Some prompt`
	wf, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.PromptTemplate != "Some prompt" {
		t.Errorf("expected 'Some prompt', got %q", wf.PromptTemplate)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	content := `---
[invalid yaml
---
prompt`
	_, err := Parse(content)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParse_NonMapFrontMatter(t *testing.T) {
	content := `---
- list
- item
---
prompt`
	_, err := Parse(content)
	if err == nil {
		t.Fatal("expected error for non-map front matter")
	}
}

func TestParse_NoClosingDelimiter(t *testing.T) {
	content := `---
key: value
no closing delimiter`
	_, err := Parse(content)
	if err == nil {
		t.Fatal("expected error for missing closing ---")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/WORKFLOW.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	err := os.WriteFile(path, []byte(`---
tracker:
  kind: linear
---
Do work on {{ issue.title }}`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	wf, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.PromptTemplate != "Do work on {{ issue.title }}" {
		t.Errorf("unexpected prompt: %q", wf.PromptTemplate)
	}
}
