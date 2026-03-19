package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-key", "test-project")
}

func TestCreateComment_Success(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		vars := req["variables"].(map[string]any)
		if vars["issueId"] != "issue-123" {
			t.Errorf("expected issueId=issue-123, got %v", vars["issueId"])
		}
		if vars["body"] != "Implementation complete." {
			t.Errorf("expected body='Implementation complete.', got %v", vars["body"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
				},
			},
		})
	})

	err := client.CreateComment(context.Background(), "issue-123", "Implementation complete.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestCreateComment_GraphQLError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{
				{"message": "unauthorized"},
			},
		})
	})

	err := client.CreateComment(context.Background(), "issue-123", "notes")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_graphql_errors: unauthorized"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestCreateComment_SuccessFalse(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": false,
				},
			},
		})
	})

	err := client.CreateComment(context.Background(), "issue-123", "notes")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_comment_failed: commentCreate returned success=false"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestCreateComment_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	err := client.CreateComment(context.Background(), "issue-123", "notes")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
