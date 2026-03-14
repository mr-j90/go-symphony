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
