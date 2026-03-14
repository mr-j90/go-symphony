package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		fmt.Println("ERROR: LINEAR_API_KEY is not set")
		os.Exit(1)
	}
	fmt.Printf("API key: %s...%s (length %d)\n\n", apiKey[:8], apiKey[len(apiKey)-4:], len(apiKey))

	// 1. Test auth by fetching the viewer
	fmt.Println("=== Testing Linear auth ===")
	resp := query(apiKey, `{ viewer { id name email } }`, nil)
	fmt.Println(resp)

	// 2. List all projects
	fmt.Println("\n=== Listing projects (name + slugId) ===")
	resp = query(apiKey, `{
		projects(first: 50) {
			nodes {
				id
				name
				slugId
				state
			}
		}
	}`, nil)
	fmt.Println(resp)

	// 3. Try the specific project slug from WORKFLOW.md
	slug := "go-cli-spec-generator"
	fmt.Printf("\n=== Searching issues for project slug: %q ===\n", slug)
	resp = query(apiKey, `query($slug: String!) {
		issues(
			filter: {
				project: { slugId: { eq: $slug } }
				state: { name: { in: ["Todo"] } }
			}
			first: 10
		) {
			nodes {
				id
				identifier
				title
				state { name }
			}
		}
	}`, map[string]any{"slug": slug})
	fmt.Println(resp)
}

func query(apiKey, q string, variables map[string]any) string {
	body := map[string]any{"query": q}
	if variables != nil {
		body["variables"] = variables
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", "https://api.linear.app/graphql", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Sprintf("ERROR: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		return pretty.String()
	}
	return string(data)
}
