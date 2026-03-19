package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jordan/go-symphony/internal/linear"
	"github.com/jordan/go-symphony/internal/orchestrator"
)

// orchClient is the subset of orchestrator.Orchestrator used by the server.
type orchClient interface {
	Snapshot() orchestrator.StateSnapshot
	TriggerPoll(ctx context.Context)
}

// videoAttacher is the subset of the Linear client used for video attachment.
type videoAttacher interface {
	FetchIssueIDByIdentifier(ctx context.Context, identifier string) (string, error)
	RequestFileUpload(ctx context.Context, filename, contentType string, size int) (*linear.FileUploadInfo, error)
	UploadFileToURL(ctx context.Context, info *linear.FileUploadInfo, contentType string, size int64, data io.Reader) error
	CreateAttachment(ctx context.Context, issueID, title, url string) error
}

// Server is the optional HTTP observability server.
type Server struct {
	orch   orchClient
	va     videoAttacher // optional; nil disables the attach-video endpoint
	logger *slog.Logger
	srv    *http.Server
	addr   string
}

// New creates a new HTTP server.
func New(orch *orchestrator.Orchestrator, va videoAttacher, port int, logger *slog.Logger) *Server {
	s := &Server{
		orch:   orch,
		va:     va,
		logger: logger,
		addr:   fmt.Sprintf("127.0.0.1:%d", port),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/v1/state", s.handleState)
	mux.HandleFunc("GET /api/v1/{identifier}", s.handleIssue)
	mux.HandleFunc("POST /api/v1/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/v1/attach-video/{identifier}", s.handleAttachVideo)

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
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

	go func() { //nolint:gosec // intentional background goroutine for graceful shutdown
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
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
.recorder { background: #16213e; padding: 1em; border-radius: 8px; margin: 1em 0; }
.recorder h2 { margin: 0 0 0.75em; color: #4ecca3; }
.recorder label { display: block; margin-bottom: 0.4em; font-size: 0.9em; color: #aaa; }
.recorder input { background: #0d1117; border: 1px solid #444; color: #e0e0e0; padding: 4px 8px; border-radius: 4px; width: 180px; }
.recorder button { margin: 0.5em 0.4em 0.5em 0; padding: 6px 14px; border: none; border-radius: 4px; cursor: pointer; font-family: monospace; font-size: 0.9em; }
#rec-start  { background: #4ecca3; color: #1a1a2e; }
#rec-stop   { background: #e94560; color: #fff; }
#rec-download, #rec-attach { background: #0f3460; color: #e0e0e0; }
button:disabled { opacity: 0.4; cursor: not-allowed; }
#rec-status { margin-top: 0.6em; font-size: 0.85em; color: #aaa; min-height: 1.2em; }
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
<tr><th>Issue</th><th>Title</th><th>State</th><th>Session</th><th>Turns</th><th>Last Event</th><th>Started</th><th>Tokens</th></tr>
`, len(snap.Running))
		for _, r := range snap.Running {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%d/%d/%d</td></tr>\n",
				r.IssueIdentifier, r.IssueTitle, r.State, r.SessionID, r.TurnCount, r.LastEvent,
				r.StartedAt.Format("15:04:05"),
				r.Tokens.InputTokens, r.Tokens.OutputTokens, r.Tokens.TotalTokens,
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	if len(snap.Retrying) > 0 {
		fmt.Fprintf(w, `<h2 class="retrying">Retrying (%d)</h2>
<table>
<tr><th>Issue</th><th>Title</th><th>Attempt</th><th>Due At</th><th>Error</th></tr>
`, len(snap.Retrying))
		for _, r := range snap.Retrying {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>\n",
				r.IssueIdentifier, r.IssueTitle, r.Attempt, r.DueAt.Format("15:04:05"), r.Error,
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	fmt.Fprintf(w, `<div class="recorder">
<h2>Screen Recording</h2>
<label for="rec-issue">Issue identifier (e.g. ZYX-75)</label>
<input id="rec-issue" type="text" placeholder="ZYX-75">
<br>
<button id="rec-start">Record</button>
<button id="rec-stop" disabled>Stop</button>
<button id="rec-download" disabled>Download</button>
<button id="rec-attach" disabled>Attach to Issue</button>
<div id="rec-status"></div>
</div>
<script>
(function(){
var mr=null,chunks=[],blob=null;
var start=document.getElementById('rec-start');
var stop=document.getElementById('rec-stop');
var dl=document.getElementById('rec-download');
var att=document.getElementById('rec-attach');
var status=document.getElementById('rec-status');
var issue=document.getElementById('rec-issue');
start.addEventListener('click',async function(){
  try{
    var stream=await navigator.mediaDevices.getDisplayMedia({video:true,audio:true});
    chunks=[];blob=null;
    mr=new MediaRecorder(stream);
    mr.ondataavailable=function(e){if(e.data.size>0)chunks.push(e.data);};
    mr.onstop=function(){
      blob=new Blob(chunks,{type:'video/webm'});
      stream.getTracks().forEach(function(t){t.stop();});
      dl.disabled=false;att.disabled=false;
      status.textContent='Recording stopped. Ready to download or attach.';
      start.disabled=false;stop.disabled=true;
    };
    mr.start();
    start.disabled=true;stop.disabled=false;dl.disabled=true;att.disabled=true;
    status.textContent='Recording...';
  }catch(err){status.textContent='Error: '+err.message;}
});
stop.addEventListener('click',function(){
  if(mr&&mr.state!=='inactive')mr.stop();
});
dl.addEventListener('click',function(){
  if(!blob)return;
  var url=URL.createObjectURL(blob);
  var a=document.createElement('a');
  a.href=url;a.download='recording.webm';a.click();
  URL.revokeObjectURL(url);
});
att.addEventListener('click',async function(){
  if(!blob)return;
  var id=issue.value.trim();
  if(!id){status.textContent='Error: enter an issue identifier.';return;}
  var fd=new FormData();
  fd.append('video',blob,'recording.webm');
  status.textContent='Uploading...';att.disabled=true;
  try{
    var resp=await fetch('/api/v1/attach-video/'+encodeURIComponent(id),{method:'POST',body:fd});
    var data=await resp.json();
    if(resp.ok){
      status.textContent='Attached! URL: '+data.attachment_url;
    }else{
      status.textContent='Error: '+(data.error&&data.error.message||resp.statusText);
      att.disabled=false;
    }
  }catch(err){status.textContent='Error: '+err.message;att.disabled=false;}
});
})();
</script>
<p><small>Generated at %s</small></p></body></html>`, snap.GeneratedAt.Format(time.RFC3339))
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

func (s *Server) handleAttachVideo(w http.ResponseWriter, r *http.Request) {
	if s.va == nil {
		writeJSON(w, http.StatusNotImplemented, errorResp("not_configured", "video attachment is not configured"))
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeJSON(w, http.StatusBadRequest, errorResp("bad_request", "missing identifier"))
		return
	}

	// Limit upload to 500 MB.
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp("bad_request", "invalid multipart form: "+err.Error()))
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp("bad_request", "missing video field: "+err.Error()))
		return
	}
	defer file.Close()

	// Buffer the file so we know its size before uploading.
	var buf bytes.Buffer
	if _, err = io.Copy(&buf, file); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResp("read_error", "failed to read video: "+err.Error()))
		return
	}
	size := buf.Len()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/webm"
	}

	ctx := r.Context()

	issueID, err := s.va.FetchIssueIDByIdentifier(ctx, identifier)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResp("issue_not_found", err.Error()))
		return
	}

	uploadInfo, err := s.va.RequestFileUpload(ctx, header.Filename, contentType, size)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResp("upload_request_failed", err.Error()))
		return
	}

	if err := s.va.UploadFileToURL(ctx, uploadInfo, contentType, int64(size), &buf); err != nil {
		writeJSON(w, http.StatusBadGateway, errorResp("upload_failed", err.Error()))
		return
	}

	title := header.Filename
	if title == "" {
		title = "Screen recording"
	}

	if err := s.va.CreateAttachment(ctx, issueID, title, uploadInfo.AssetURL); err != nil {
		writeJSON(w, http.StatusBadGateway, errorResp("attachment_failed", err.Error()))
		return
	}

	if s.logger != nil {
		s.logger.Info("video attached to issue", "identifier", identifier, "url", uploadInfo.AssetURL)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attachment_url": uploadInfo.AssetURL,
		"issue_id":       issueID,
		"identifier":     identifier,
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
	_ = json.NewEncoder(w).Encode(v)
}
