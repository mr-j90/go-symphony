package workflow

import (
	"fmt"
	"os"
	"strings"

	"github.com/jordan/go-symphony/internal/model"
	"gopkg.in/yaml.v3"
)

// Load reads a WORKFLOW.md file and returns the parsed workflow definition.
func Load(path string) (*model.WorkflowDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing_workflow_file: %s", path)
		}
		return nil, fmt.Errorf("missing_workflow_file: %w", err)
	}
	return Parse(string(data))
}

// Parse splits a WORKFLOW.md content string into config map and prompt template.
func Parse(content string) (*model.WorkflowDefinition, error) {
	config := map[string]any{}
	promptBody := content

	if strings.HasPrefix(content, "---") {
		// Find the closing ---
		rest := content[3:]
		idx := strings.Index(rest, "\n---")
		if idx < 0 {
			return nil, fmt.Errorf("workflow_parse_error: no closing --- for front matter")
		}

		frontMatter := rest[:idx]
		promptBody = strings.TrimLeft(rest[idx+4:], "\n")

		if err := yaml.Unmarshal([]byte(frontMatter), &config); err != nil {
			return nil, fmt.Errorf("workflow_parse_error: invalid YAML front matter: %w", err)
		}
		if config == nil {
			return nil, fmt.Errorf("workflow_front_matter_not_a_map: front matter decoded to nil")
		}
	}

	return &model.WorkflowDefinition{
		Config:         config,
		PromptTemplate: strings.TrimSpace(promptBody),
	}, nil
}
