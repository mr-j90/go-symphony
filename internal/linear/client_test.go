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

func TestFetchTeamID_Success(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		vars := req["variables"].(map[string]any)
		if vars["issueId"] != "issue-abc" {
			t.Errorf("expected issueId=issue-abc, got %v", vars["issueId"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{"id": "team-xyz"},
				},
			},
		})
	})

	teamID, err := client.FetchTeamID(context.Background(), "issue-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if teamID != "team-xyz" {
		t.Errorf("expected team-xyz, got %s", teamID)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestFetchTeamID_GraphQLError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "not found"}},
		})
	})

	_, err := client.FetchTeamID(context.Background(), "issue-abc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_graphql_errors: not found"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestFetchTeamID_EmptyTeam(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{"id": ""},
				},
			},
		})
	})

	_, err := client.FetchTeamID(context.Background(), "issue-abc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_team_not_found: no team found for issue issue-abc"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestCreateIssue_Success(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		vars := req["variables"].(map[string]any)
		if vars["teamId"] != "team-xyz" {
			t.Errorf("expected teamId=team-xyz, got %v", vars["teamId"])
		}
		if vars["title"] != "Bug: nil pointer in handler" {
			t.Errorf("expected title='Bug: nil pointer in handler', got %v", vars["title"])
		}
		if vars["description"] != "Details here" {
			t.Errorf("expected description='Details here', got %v", vars["description"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueCreate": map[string]any{
					"success": true,
					"issue":   map[string]any{"id": "new-id-1", "identifier": "ZYX-99"},
				},
			},
		})
	})

	identifier, err := client.CreateIssue(context.Background(), "team-xyz", "Bug: nil pointer in handler", "Details here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identifier != "ZYX-99" {
		t.Errorf("expected ZYX-99, got %s", identifier)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestCreateIssue_GraphQLError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "unauthorized"}},
		})
	})

	_, err := client.CreateIssue(context.Background(), "team-xyz", "title", "desc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_graphql_errors: unauthorized"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestCreateIssue_SuccessFalse(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueCreate": map[string]any{
					"success": false,
					"issue":   nil,
				},
			},
		})
	})

	_, err := client.CreateIssue(context.Background(), "team-xyz", "title", "desc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "linear_create_issue_failed: issueCreate returned success=false"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}
