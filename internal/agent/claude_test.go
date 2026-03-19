package agent

import (
	"testing"
)

func TestExtractClaudeUsage_ResultEvent(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"abc","usage":{"input_tokens":1000,"output_tokens":500}}`)
	u := extractClaudeUsage(raw)
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.InputTokens != 1000 {
		t.Errorf("InputTokens: want 1000, got %d", u.InputTokens)
	}
	if u.OutputTokens != 500 {
		t.Errorf("OutputTokens: want 500, got %d", u.OutputTokens)
	}
	if u.TotalTokens != 1500 {
		t.Errorf("TotalTokens: want 1500, got %d", u.TotalTokens)
	}
}

func TestExtractClaudeUsage_AssistantEvent(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"id":"msg_01","role":"assistant","content":[],"usage":{"input_tokens":800,"output_tokens":200}}}`)
	u := extractClaudeUsage(raw)
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.InputTokens != 800 {
		t.Errorf("InputTokens: want 800, got %d", u.InputTokens)
	}
	if u.OutputTokens != 200 {
		t.Errorf("OutputTokens: want 200, got %d", u.OutputTokens)
	}
	if u.TotalTokens != 1000 {
		t.Errorf("TotalTokens: want 1000, got %d", u.TotalTokens)
	}
}

func TestExtractClaudeUsage_NoUsage(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"init","session_id":"abc"}`)
	u := extractClaudeUsage(raw)
	if u != nil {
		t.Errorf("expected nil usage for system event, got %+v", u)
	}
}

func TestExtractClaudeUsage_ZeroTokens(t *testing.T) {
	raw := []byte(`{"type":"result","usage":{"input_tokens":0,"output_tokens":0}}`)
	u := extractClaudeUsage(raw)
	if u != nil {
		t.Errorf("expected nil for zero-token usage, got %+v", u)
	}
}

func TestExtractClaudeUsage_InvalidJSON(t *testing.T) {
	u := extractClaudeUsage([]byte(`not json`))
	if u != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", u)
	}
}

func TestExtractClaudeUsage_WithCacheTokens(t *testing.T) {
	raw := []byte(`{"type":"result","usage":{"input_tokens":600,"output_tokens":300,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}`)
	u := extractClaudeUsage(raw)
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.InputTokens != 600 {
		t.Errorf("InputTokens: want 600, got %d", u.InputTokens)
	}
	if u.OutputTokens != 300 {
		t.Errorf("OutputTokens: want 300, got %d", u.OutputTokens)
	}
	if u.TotalTokens != 900 {
		t.Errorf("TotalTokens: want 900, got %d", u.TotalTokens)
	}
}
