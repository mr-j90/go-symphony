package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordan/go-symphony/internal/model"
	"github.com/jordan/go-symphony/internal/orchestrator"
)

type fakeOrch struct {
	snap orchestrator.StateSnapshot
}

func (f *fakeOrch) Snapshot() orchestrator.StateSnapshot { return f.snap }
func (f *fakeOrch) TriggerPoll(_ context.Context)        {}

func newTestServer(snap orchestrator.StateSnapshot) *httptest.Server {
	s := &Server{orch: &fakeOrch{snap: snap}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	return httptest.NewServer(mux)
}

func TestDashboardShowsTokenCounts(t *testing.T) {
	now := time.Now()
	snap := orchestrator.StateSnapshot{
		GeneratedAt: now,
		Running: []orchestrator.RunningSnapshot{
			{
				IssueID:         "id-1",
				IssueIdentifier: "ZYX-42",
				IssueTitle:      "Some issue",
				State:           "In Progress",
				StartedAt:       now,
				Tokens: orchestrator.TokenSnapshot{
					InputTokens:  1234,
					OutputTokens: 567,
					TotalTokens:  1801,
				},
			},
		},
		CodexTotals: model.CodexTotals{
			InputTokens:  5000,
			OutputTokens: 2000,
			TotalTokens:  7000,
		},
	}

	ts := newTestServer(snap)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/") //nolint:noctx // test-only HTTP call
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	html := string(body)

	// Aggregate totals in header
	for _, want := range []string{"5000", "2000", "7000"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing aggregate token count %q", want)
		}
	}

	// Per-session tokens in running table
	for _, want := range []string{"1234", "567", "1801"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing per-session token count %q", want)
		}
	}
}

func TestDashboardShowsIssueTitle(t *testing.T) {
	now := time.Now()
	snap := orchestrator.StateSnapshot{
		GeneratedAt: now,
		Running: []orchestrator.RunningSnapshot{
			{
				IssueID:         "id-1",
				IssueIdentifier: "ZYX-99",
				IssueTitle:      "Fix the login bug",
				State:           "In Progress",
				StartedAt:       now,
			},
		},
		Retrying: []orchestrator.RetrySnapshot{
			{
				IssueID:         "id-2",
				IssueIdentifier: "ZYX-100",
				IssueTitle:      "Add dark mode",
				Attempt:         1,
				DueAt:           now.Add(10 * time.Second),
				Error:           "timeout",
			},
		},
	}

	ts := newTestServer(snap)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/") //nolint:noctx // test-only HTTP call
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	html := string(body)

	for _, want := range []string{"Fix the login bug", "ZYX-99", "Add dark mode", "ZYX-100"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}
