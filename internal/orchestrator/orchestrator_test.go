package orchestrator

import (
	"testing"
	"time"

	"github.com/jordan/go-symphony/internal/model"
)

func intPtr(n int) *int {
	return &n
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestSortForDispatch(t *testing.T) {
	now := time.Now()

	issues := []model.Issue{
		{Identifier: "D", Priority: nil, CreatedAt: timePtr(now)},
		{Identifier: "A", Priority: intPtr(1), CreatedAt: timePtr(now.Add(-2 * time.Hour))},
		{Identifier: "C", Priority: intPtr(3), CreatedAt: timePtr(now.Add(-1 * time.Hour))},
		{Identifier: "B", Priority: intPtr(1), CreatedAt: timePtr(now.Add(-1 * time.Hour))},
	}

	sortForDispatch(issues)

	expected := []string{"A", "B", "C", "D"}
	for i, exp := range expected {
		if issues[i].Identifier != exp {
			t.Errorf("position %d: expected %s, got %s", i, exp, issues[i].Identifier)
		}
	}
}

func TestSortForDispatch_SamePrioritySameTime(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{Identifier: "Z", Priority: intPtr(1), CreatedAt: timePtr(now)},
		{Identifier: "A", Priority: intPtr(1), CreatedAt: timePtr(now)},
		{Identifier: "M", Priority: intPtr(1), CreatedAt: timePtr(now)},
	}

	sortForDispatch(issues)

	expected := []string{"A", "M", "Z"}
	for i, exp := range expected {
		if issues[i].Identifier != exp {
			t.Errorf("position %d: expected %s, got %s", i, exp, issues[i].Identifier)
		}
	}
}

func TestSnapshotIncludesIssueTitle(t *testing.T) {
	o := &Orchestrator{
		running:       make(map[string]*model.RunningEntry),
		retryAttempts: make(map[string]*model.RetryEntry),
	}

	o.running["issue-1"] = &model.RunningEntry{
		IssueID:    "issue-1",
		Identifier: "ABC-1",
		Issue: model.Issue{
			ID:         "issue-1",
			Identifier: "ABC-1",
			Title:      "Fix the login bug",
			State:      "In Progress",
		},
		StartedAt: time.Now(),
	}

	o.retryAttempts["issue-2"] = &model.RetryEntry{
		IssueID:    "issue-2",
		Identifier: "ABC-2",
		IssueTitle: "Add dark mode",
		Attempt:    1,
		DueAtMS:    time.Now().Add(10 * time.Second).UnixMilli(),
		Error:      "timeout",
	}

	snap := o.Snapshot()

	if len(snap.Running) != 1 {
		t.Fatalf("expected 1 running entry, got %d", len(snap.Running))
	}
	if snap.Running[0].IssueTitle != "Fix the login bug" {
		t.Errorf("running snapshot: expected title %q, got %q", "Fix the login bug", snap.Running[0].IssueTitle)
	}
	if snap.Running[0].IssueIdentifier != "ABC-1" {
		t.Errorf("running snapshot: expected identifier %q, got %q", "ABC-1", snap.Running[0].IssueIdentifier)
	}

	if len(snap.Retrying) != 1 {
		t.Fatalf("expected 1 retrying entry, got %d", len(snap.Retrying))
	}
	if snap.Retrying[0].IssueTitle != "Add dark mode" {
		t.Errorf("retry snapshot: expected title %q, got %q", "Add dark mode", snap.Retrying[0].IssueTitle)
	}
	if snap.Retrying[0].IssueIdentifier != "ABC-2" {
		t.Errorf("retry snapshot: expected identifier %q, got %q", "ABC-2", snap.Retrying[0].IssueIdentifier)
	}
}

func TestSortForDispatch_NilPriorityLast(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{Identifier: "B", Priority: nil, CreatedAt: timePtr(now)},
		{Identifier: "A", Priority: intPtr(4), CreatedAt: timePtr(now)},
	}

	sortForDispatch(issues)

	if issues[0].Identifier != "A" {
		t.Errorf("expected A first (has priority), got %s", issues[0].Identifier)
	}
	if issues[1].Identifier != "B" {
		t.Errorf("expected B second (nil priority), got %s", issues[1].Identifier)
	}
}
