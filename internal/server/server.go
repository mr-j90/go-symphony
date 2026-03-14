package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jordan/go-symphony/internal/orchestrator"
)

// Server is the optional HTTP observability server.
type Server struct {
	orch   *orchestrator.Orchestrator
	logger *slog.Logger
	srv    *http.Server
	addr   string
}

// New creates a new HTTP server.
func New(orch *orchestrator.Orchestrator, port int, logger *slog.Logger) *Server {
	s := &Server{
		orch:   orch,
		logger: logger,
		addr:   fmt.Sprintf("127.0.0.1:%d", port),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/v1/state", s.handleState)
	mux.HandleFunc("GET /api/v1/{identifier}", s.handleIssue)
	mux.HandleFunc("POST /api/v1/refresh", s.handleRefresh)

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	return s
}

// Start starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("http listen: %w", err)
	}
	s.addr = ln.Addr().String()
	s.logger.Info("http server started", "addr", s.addr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.srv.Shutdown(shutdownCtx)
	}()

	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("http server error", "error", err)
		}
	}()

	return nil
}

// Addr returns the actual listen address.
func (s *Server) Addr() string {
	return s.addr
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snap := s.orch.Snapshot()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Symphony Dashboard</title>
<meta http-equiv="refresh" content="5">
<style>
body { font-family: monospace; margin: 2em; background: #1a1a2e; color: #e0e0e0; }
h1 { color: #0f3460; }
table { border-collapse: collapse; width: 100%%; margin: 1em 0; }
th, td { border: 1px solid #333; padding: 8px; text-align: left; }
th { background: #16213e; }
.running { color: #4ecca3; }
.retrying { color: #e94560; }
.totals { background: #16213e; padding: 1em; border-radius: 8px; display: inline-block; margin: 1em 0; }
</style>
</head>
<body>
<h1>Symphony</h1>
<div class="totals">
<strong>Totals:</strong> %d running, %d retrying |
Tokens: %d in / %d out / %d total |
Runtime: %.1fs
</div>
`,
		len(snap.Running), len(snap.Retrying),
		snap.CodexTotals.InputTokens, snap.CodexTotals.OutputTokens, snap.CodexTotals.TotalTokens,
		snap.CodexTotals.SecondsRunning,
	)

	if len(snap.Running) > 0 {
		fmt.Fprintf(w, `<h2 class="running">Running (%d)</h2>
<table>
<tr><th>Issue</th><th>State</th><th>Session</th><th>Turns</th><th>Last Event</th><th>Started</th><th>Tokens</th></tr>
`, len(snap.Running))
		for _, r := range snap.Running {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%d/%d/%d</td></tr>\n",
				r.IssueIdentifier, r.State, r.SessionID, r.TurnCount, r.LastEvent,
				r.StartedAt.Format("15:04:05"),
				r.Tokens.InputTokens, r.Tokens.OutputTokens, r.Tokens.TotalTokens,
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	if len(snap.Retrying) > 0 {
		fmt.Fprintf(w, `<h2 class="retrying">Retrying (%d)</h2>
<table>
<tr><th>Issue</th><th>Attempt</th><th>Due At</th><th>Error</th></tr>
`, len(snap.Retrying))
		for _, r := range snap.Retrying {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>\n",
				r.IssueIdentifier, r.Attempt, r.DueAt.Format("15:04:05"), r.Error,
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	fmt.Fprintf(w, "<p><small>Generated at %s</small></p></body></html>", snap.GeneratedAt.Format(time.RFC3339))
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.orch.Snapshot()

	resp := map[string]any{
		"generated_at": snap.GeneratedAt,
		"counts": map[string]int{
			"running":  len(snap.Running),
			"retrying": len(snap.Retrying),
		},
		"running":      snap.Running,
		"retrying":     snap.Retrying,
		"codex_totals": snap.CodexTotals,
		"rate_limits":  snap.RateLimits,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeJSON(w, http.StatusBadRequest, errorResp("bad_request", "missing identifier"))
		return
	}

	snap := s.orch.Snapshot()

	// Search in running
	for _, entry := range snap.Running {
		if strings.EqualFold(entry.IssueIdentifier, identifier) {
			resp := map[string]any{
				"issue_identifier": entry.IssueIdentifier,
				"issue_id":         entry.IssueID,
				"status":           "running",
				"running":          entry,
				"retry":            nil,
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	// Search in retrying
	for _, entry := range snap.Retrying {
		if strings.EqualFold(entry.IssueIdentifier, identifier) {
			resp := map[string]any{
				"issue_identifier": entry.IssueIdentifier,
				"issue_id":         entry.IssueID,
				"status":           "retrying",
				"running":          nil,
				"retry":            entry,
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, errorResp("issue_not_found", fmt.Sprintf("issue %s not found in current state", identifier)))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.orch.TriggerPoll(r.Context())

	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":       true,
		"coalesced":    false,
		"requested_at": time.Now().UTC(),
		"operations":   []string{"poll", "reconcile"},
	})
}

func errorResp(code, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
