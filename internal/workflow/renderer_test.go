package workflow

import (
	"strings"
	"testing"

	"github.com/jordan/go-symphony/internal/model"
)

func TestRenderPrompt_Basic(t *testing.T) {
	tmpl := "Work on {{ issue.identifier }}: {{ issue.title }}"
	issue := model.Issue{
		ID:         "abc123",
		Identifier: "MT-100",
		Title:      "Fix the bug",
		State:      "Todo",
		Labels:     []string{"bug"},
	}

	result, err := RenderPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Work on MT-100: Fix the bug" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestRenderPrompt_WithAttempt(t *testing.T) {
	tmpl := "{% if attempt %}Retry attempt {{ attempt }}. {% endif %}Work on {{ issue.identifier }}"
	issue := model.Issue{
		ID:         "abc123",
		Identifier: "MT-100",
		Title:      "Fix the bug",
		State:      "Todo",
	}

	// First attempt (nil)
	result, err := RenderPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Retry") {
		t.Errorf("first attempt should not contain retry text: %q", result)
	}

	// Retry attempt
	attempt := 2
	result, err = RenderPrompt(tmpl, issue, &attempt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Retry attempt 2") {
		t.Errorf("expected retry text: %q", result)
	}
}

func TestRenderPrompt_EmptyTemplate(t *testing.T) {
	result, err := RenderPrompt("", model.Issue{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "You are working on an issue from Linear." {
		t.Errorf("expected fallback prompt, got %q", result)
	}
}

func TestRenderPrompt_WithLabels(t *testing.T) {
	tmpl := "Labels: {% for label in issue.labels %}{{ label }} {% endfor %}"
	issue := model.Issue{
		ID:         "abc123",
		Identifier: "MT-100",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{"bug", "priority"},
	}

	result, err := RenderPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "bug") || !strings.Contains(result, "priority") {
		t.Errorf("expected labels in output: %q", result)
	}
}

func TestRenderPrompt_WithDescription(t *testing.T) {
	tmpl := "{{ issue.title }}: {{ issue.description }}"
	desc := "Some description"
	issue := model.Issue{
		ID:          "abc123",
		Identifier:  "MT-100",
		Title:       "Test",
		Description: &desc,
		State:       "Todo",
	}

	result, err := RenderPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Some description") {
		t.Errorf("expected description: %q", result)
	}
}
