package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jordan/go-symphony/internal/model"
)

const (
	defaultPageSize = 50
	networkTimeout  = 30 * time.Second
)

// Client is the Linear GraphQL API client.
type Client struct {
	endpoint    string
	apiKey      string
	projectSlug string
	httpClient  *http.Client
}

// NewClient creates a new Linear client.
func NewClient(endpoint, apiKey, projectSlug string) *Client {
	return &Client{
		endpoint:    endpoint,
		apiKey:      apiKey,
		projectSlug: projectSlug,
		httpClient: &http.Client{
			Timeout: networkTimeout,
		},
	}
}

// FetchCandidateIssues fetches issues in active states for the configured project.
func (c *Client) FetchCandidateIssues(ctx context.Context, activeStates []string) ([]model.Issue, error) {
	var allIssues []model.Issue
	var cursor *string

	for {
		issues, nextCursor, err := c.fetchIssuePage(ctx, activeStates, cursor)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, issues...)
		if nextCursor == nil {
			break
		}
		cursor = nextCursor
	}

	return allIssues, nil
}

// FetchIssueStatesByIDs fetches current states for specific issue IDs (reconciliation).
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]model.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	query := `query($ids: [ID!]!) {
		issues(filter: { id: { in: $ids } }) {
			nodes {
				id
				identifier
				title
				state { name }
				labels { nodes { name } }
			}
		}
	}`

	variables := map[string]any{
		"ids": issueIDs,
	}

	resp, err := c.doQuery(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data struct {
			Issues struct {
				Nodes []issueNode `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("linear_unknown_payload: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("linear_graphql_errors: %s", result.Errors[0].Message)
	}

	issues := make([]model.Issue, 0, len(result.Data.Issues.Nodes))
	for _, n := range result.Data.Issues.Nodes {
		issues = append(issues, normalizeIssueNode(n))
	}
	return issues, nil
}

// FetchIssuesByStates fetches issues in specified states (used for terminal cleanup).
func (c *Client) FetchIssuesByStates(ctx context.Context, states []string) ([]model.Issue, error) {
	if len(states) == 0 {
		return nil, nil
	}

	var allIssues []model.Issue
	var cursor *string

	for {
		query := `query($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $after: String) {
			issues(
				filter: {
					project: { slugId: { eq: $projectSlug } }
					state: { name: { in: $stateNames } }
				}
				first: $first
				after: $after
			) {
				nodes {
					id
					identifier
					title
					state { name }
				}
				pageInfo {
					hasNextPage
					endCursor
				}
			}
		}`

		variables := map[string]any{
			"projectSlug": c.projectSlug,
			"stateNames":  states,
			"first":       defaultPageSize,
		}
		if cursor != nil {
			variables["after"] = *cursor
		}

		resp, err := c.doQuery(ctx, query, variables)
		if err != nil {
			return nil, err
		}

		var result struct {
			Data struct {
				Issues struct {
					Nodes    []issueNode `json:"nodes"`
					PageInfo pageInfo    `json:"pageInfo"`
				} `json:"issues"`
			} `json:"data"`
			Errors []graphqlError `json:"errors"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("linear_unknown_payload: %w", err)
		}
		if len(result.Errors) > 0 {
			return nil, fmt.Errorf("linear_graphql_errors: %s", result.Errors[0].Message)
		}

		for _, n := range result.Data.Issues.Nodes {
			allIssues = append(allIssues, normalizeIssueNode(n))
		}

		if !result.Data.Issues.PageInfo.HasNextPage {
			break
		}
		if result.Data.Issues.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("linear_missing_end_cursor")
		}
		c := result.Data.Issues.PageInfo.EndCursor
		cursor = &c
	}

	return allIssues, nil
}

// ExecuteGraphQL runs a raw GraphQL query (for the linear_graphql tool extension).
func (c *Client) ExecuteGraphQL(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	return c.doQuery(ctx, query, variables)
}

// TransitionIssueState moves an issue to a target state by name.
// It first resolves the workflow state ID for the issue's team, then updates the issue.
func (c *Client) TransitionIssueState(ctx context.Context, issueID, targetStateName string) error {
	// Step 1: Get the issue's team ID and current workflow states
	stateQuery := `query($issueId: String!) {
		issue(id: $issueId) {
			team {
				states {
					nodes {
						id
						name
					}
				}
			}
		}
	}`

	resp, err := c.doQuery(ctx, stateQuery, map[string]any{"issueId": issueID})
	if err != nil {
		return fmt.Errorf("linear_api_request: fetch states: %w", err)
	}

	var stateResult struct {
		Data struct {
			Issue struct {
				Team struct {
					States struct {
						Nodes []struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"states"`
				} `json:"team"`
			} `json:"issue"`
		} `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(resp, &stateResult); err != nil {
		return fmt.Errorf("linear_unknown_payload: %w", err)
	}
	if len(stateResult.Errors) > 0 {
		return fmt.Errorf("linear_graphql_errors: %s", stateResult.Errors[0].Message)
	}

	// Step 2: Find the target state ID
	var targetStateID string
	for _, s := range stateResult.Data.Issue.Team.States.Nodes {
		if strings.EqualFold(s.Name, targetStateName) {
			targetStateID = s.ID
			break
		}
	}
	if targetStateID == "" {
		return fmt.Errorf("linear_state_not_found: no workflow state %q found for issue's team", targetStateName)
	}

	// Step 3: Update the issue state
	mutation := `mutation($issueId: String!, $stateId: String!) {
		issueUpdate(id: $issueId, input: { stateId: $stateId }) {
			success
			issue {
				id
				state { name }
			}
		}
	}`

	mutResp, err := c.doQuery(ctx, mutation, map[string]any{
		"issueId": issueID,
		"stateId": targetStateID,
	})
	if err != nil {
		return fmt.Errorf("linear_api_request: update state: %w", err)
	}

	var mutResult struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		} `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(mutResp, &mutResult); err != nil {
		return fmt.Errorf("linear_unknown_payload: %w", err)
	}
	if len(mutResult.Errors) > 0 {
		return fmt.Errorf("linear_graphql_errors: %s", mutResult.Errors[0].Message)
	}
	if !mutResult.Data.IssueUpdate.Success {
		return fmt.Errorf("linear_state_transition_failed: issueUpdate returned success=false")
	}

	return nil
}

func (c *Client) fetchIssuePage(ctx context.Context, activeStates []string, cursor *string) ([]model.Issue, *string, error) {
	query := `query($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $after: String) {
		issues(
			filter: {
				project: { slugId: { eq: $projectSlug } }
				state: { name: { in: $stateNames } }
			}
			first: $first
			after: $after
			orderBy: createdAt
		) {
			nodes {
				id
				identifier
				title
				description
				priority
				state { name }
				branchName
				url
				labels { nodes { name } }
				relations(first: 50) {
					nodes {
						type
						relatedIssue {
							id
							identifier
							state { name }
						}
					}
				}
				createdAt
				updatedAt
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}`

	variables := map[string]any{
		"projectSlug": c.projectSlug,
		"stateNames":  activeStates,
		"first":       defaultPageSize,
	}
	if cursor != nil {
		variables["after"] = *cursor
	}

	resp, err := c.doQuery(ctx, query, variables)
	if err != nil {
		return nil, nil, err
	}

	var result struct {
		Data struct {
			Issues struct {
				Nodes    []issueNode `json:"nodes"`
				PageInfo pageInfo    `json:"pageInfo"`
			} `json:"issues"`
		} `json:"data"`
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, nil, fmt.Errorf("linear_unknown_payload: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, nil, fmt.Errorf("linear_graphql_errors: %s", result.Errors[0].Message)
	}

	issues := make([]model.Issue, 0, len(result.Data.Issues.Nodes))
	for _, n := range result.Data.Issues.Nodes {
		issues = append(issues, normalizeIssueNode(n))
	}

	var nextCursor *string
	if result.Data.Issues.PageInfo.HasNextPage {
		if result.Data.Issues.PageInfo.EndCursor == "" {
			return nil, nil, fmt.Errorf("linear_missing_end_cursor")
		}
		nextCursor = &result.Data.Issues.PageInfo.EndCursor
	}

	return issues, nextCursor, nil
}

func (c *Client) doQuery(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	body := map[string]any{
		"query": query,
	}
	if variables != nil {
		body["variables"] = variables
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: marshal error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear_api_status: %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// internal types for JSON deserialization

type issueNode struct {
	ID          string  `json:"id"`
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Priority    any     `json:"priority"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
	BranchName *string `json:"branchName"`
	URL        *string `json:"url"`
	Labels     struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Relations struct {
		Nodes []struct {
			Type         string `json:"type"`
			RelatedIssue struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
	CreatedAt *string `json:"createdAt"`
	UpdatedAt *string `json:"updatedAt"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type graphqlError struct {
	Message string `json:"message"`
}

func normalizeIssueNode(n issueNode) model.Issue {
	issue := model.Issue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		State:       n.State.Name,
		BranchName:  n.BranchName,
		URL:         n.URL,
	}

	// Priority: integer only
	if n.Priority != nil {
		switch v := n.Priority.(type) {
		case float64:
			p := int(v)
			issue.Priority = &p
		case int:
			issue.Priority = &v
		}
	}

	// Labels: normalized to lowercase
	labels := make([]string, 0, len(n.Labels.Nodes))
	for _, l := range n.Labels.Nodes {
		labels = append(labels, strings.ToLower(l.Name))
	}
	issue.Labels = labels

	// BlockedBy: inverse relations of type "blocks"
	for _, rel := range n.Relations.Nodes {
		if rel.Type == "blocks" {
			id := rel.RelatedIssue.ID
			ident := rel.RelatedIssue.Identifier
			state := rel.RelatedIssue.State.Name
			issue.BlockedBy = append(issue.BlockedBy, model.BlockerRef{
				ID:         &id,
				Identifier: &ident,
				State:      &state,
			})
		}
	}

	// Timestamps
	if n.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *n.CreatedAt); err == nil {
			issue.CreatedAt = &t
		}
	}
	if n.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *n.UpdatedAt); err == nil {
			issue.UpdatedAt = &t
		}
	}

	return issue
}
