package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordan/go-symphony/internal/linear"
	"github.com/jordan/go-symphony/internal/model"
	"github.com/jordan/go-symphony/internal/orchestrator"
)

type fakeOrch struct {
	snap orchestrator.StateSnapshot
}

func (f *fakeOrch) Snapshot() orchestrator.StateSnapshot { return f.snap }
func (f *fakeOrch) TriggerPoll(_ context.Context)        {}

// fakeVA is a test implementation of videoAttacher.
type fakeVA struct {
	issueID       string
	fetchErr      error
	uploadInfo    *linear.FileUploadInfo
	uploadReqErr  error
	uploadFileErr error
	attachErr     error
	attached      bool
}

func (f *fakeVA) FetchIssueIDByIdentifier(_ context.Context, _ string) (string, error) {
	return f.issueID, f.fetchErr
}
func (f *fakeVA) RequestFileUpload(_ context.Context, _, _ string, _ int) (*linear.FileUploadInfo, error) {
	return f.uploadInfo, f.uploadReqErr
}
func (f *fakeVA) UploadFileToURL(_ context.Context, _ *linear.FileUploadInfo, _ string, _ int64, _ io.Reader) error {
	return f.uploadFileErr
}
func (f *fakeVA) CreateAttachment(_ context.Context, _, _, _ string) error {
	f.attached = true
	return f.attachErr
}

func newTestServer(snap orchestrator.StateSnapshot) *httptest.Server {
	s := &Server{orch: &fakeOrch{snap: snap}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	return httptest.NewServer(mux)
}

func newTestServerWithVA(snap orchestrator.StateSnapshot, va videoAttacher) *httptest.Server {
	s := &Server{orch: &fakeOrch{snap: snap}, va: va}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("POST /api/v1/attach-video/{identifier}", s.handleAttachVideo)
	return httptest.NewServer(mux)
}

// buildVideoUpload creates a multipart form body containing a small fake video file.
func buildVideoUpload(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("video", "recording.webm")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write([]byte("fake-video-bytes")) //nolint:errcheck
	mw.Close()
	return &buf, mw.FormDataContentType()
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

func TestDashboardShowsRecordingUI(t *testing.T) {
	ts := newTestServer(orchestrator.StateSnapshot{GeneratedAt: time.Now()})
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/") //nolint:noctx // test-only HTTP call
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	for _, want := range []string{"rec-start", "rec-stop", "rec-download", "rec-attach", "getDisplayMedia", "MediaRecorder"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing recording UI element %q", want)
		}
	}
}

func TestAttachVideo_NoVA(t *testing.T) {
	// Server with no videoAttacher should return 501.
	s := &Server{orch: &fakeOrch{}, logger: nil}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/attach-video/{identifier}", s.handleAttachVideo)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	buf, ct := buildVideoUpload(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/attach-video/ZYX-75", buf) //nolint:noctx
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", resp.StatusCode)
	}
}

func TestAttachVideo_Success(t *testing.T) {
	va := &fakeVA{
		issueID: "uuid-abc",
		uploadInfo: &linear.FileUploadInfo{
			UploadURL: "https://s3.example.com/upload",
			AssetURL:  "https://cdn.example.com/rec.webm",
		},
	}
	ts := newTestServerWithVA(orchestrator.StateSnapshot{GeneratedAt: time.Now()}, va)
	defer ts.Close()

	buf, ct := buildVideoUpload(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/attach-video/ZYX-75", buf) //nolint:noctx
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if !va.attached {
		t.Error("expected CreateAttachment to be called")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "cdn.example.com") {
		t.Errorf("response missing asset URL: %s", body)
	}
}

func TestAttachVideo_IssueNotFound(t *testing.T) {
	va := &fakeVA{fetchErr: fmt.Errorf("linear_issue_not_found: no issue with identifier \"ZYX-99\"")}
	ts := newTestServerWithVA(orchestrator.StateSnapshot{GeneratedAt: time.Now()}, va)
	defer ts.Close()

	buf, ct := buildVideoUpload(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/attach-video/ZYX-99", buf) //nolint:noctx
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAttachVideo_MissingVideoField(t *testing.T) {
	va := &fakeVA{issueID: "uuid-abc"}
	ts := newTestServerWithVA(orchestrator.StateSnapshot{GeneratedAt: time.Now()}, va)
	defer ts.Close()

	// Send multipart without a "video" field.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/attach-video/ZYX-75", &buf) //nolint:noctx
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
