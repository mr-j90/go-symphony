package workflow

import (
	"fmt"
	"strings"

	"github.com/jordan/go-symphony/internal/model"
	"github.com/osteele/liquid"
)

var engine *liquid.Engine

func init() {
	engine = liquid.NewEngine()
}

// RenderPrompt renders the workflow prompt template with the given issue and attempt.
func RenderPrompt(tmpl string, issue model.Issue, attempt *int) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return "You are working on an issue from Linear.", nil
	}

	bindings := map[string]any{
		"issue":   issueToMap(issue),
		"attempt": attempt,
	}

	out, err := engine.ParseAndRenderString(tmpl, bindings)
	if err != nil {
		return "", fmt.Errorf("template_render_error: %w", err)
	}
	return out, nil
}

func issueToMap(issue model.Issue) map[string]any {
	m := map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": ptrStr(issue.Description),
		"priority":    ptrInt(issue.Priority),
		"state":       issue.State,
		"branch_name": ptrStr(issue.BranchName),
		"url":         ptrStr(issue.URL),
		"labels":      issue.Labels,
	}

	blockers := make([]map[string]any, len(issue.BlockedBy))
	for i, b := range issue.BlockedBy {
		blockers[i] = map[string]any{
			"id":         ptrStr(b.ID),
			"identifier": ptrStr(b.Identifier),
			"state":      ptrStr(b.State),
		}
	}
	m["blocked_by"] = blockers

	if issue.CreatedAt != nil {
		m["created_at"] = issue.CreatedAt.Format("2006-01-02T15:04:05Z")
	} else {
		m["created_at"] = nil
	}
	if issue.UpdatedAt != nil {
		m["updated_at"] = issue.UpdatedAt.Format("2006-01-02T15:04:05Z")
	} else {
		m["updated_at"] = nil
	}
	return m
}

func ptrStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
