---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: go-cli-spec-generator-e07f133be8a2
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Closed
    - Cancelled
    - Canceled
    - Duplicate

polling:
  interval_ms: 30000

workspace:
  root: ./workspaces/

hooks:
  after_create: |
    git clone git@github.com:mr-j90/go-cli-spec-generator.git .
  before_run: |
    git fetch origin
    git checkout master
    git pull
  timeout_ms: 120000

agent:
  type: claude_code
  dispatch_transition_state: In Progress
  max_concurrent_agents: 5
  max_turns: 20
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    todo: 3
    in progress: 5

claude_code:
  model: sonnet
  permission_mode: auto
  allowed_tools:
    - Bash
    - Read
    - Edit
    - Write
    - Glob
    - Grep
  max_turns: 10
  turn_timeout_ms: 3600000

server:
  port: 8080
---
You are an autonomous coding agent working on issue {{ issue.identifier }}: {{ issue.title }}.

{% if issue.description %}
## Issue Description
{{ issue.description }}
{% endif %}

{% if issue.labels.size > 0 %}
Labels: {% for label in issue.labels %}{{ label }}{% unless forloop.last %}, {% endunless %}{% endfor %}
{% endif %}

{% if issue.url %}
Issue URL: {{ issue.url }}
{% endif %}

{% if attempt %}
This is retry attempt {{ attempt }}. Review your previous work and continue from where you left off.
{% endif %}

## Instructions
1. Create a feature branch named `{{ issue.identifier | downcase }}` before making any changes
2. Read and understand the issue requirements
3. Implement the necessary changes
4. Commit your work frequently with clear commit messages
5. Write tests for your changes
6. Ensure all tests pass
7. Push your branch and create a pull request with a clear description using `gh pr create`
8. Move the issue to "In Review" when complete
