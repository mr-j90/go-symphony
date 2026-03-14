# go-symphony User Guide

go-symphony is a long-running automation service that polls Linear for issues, creates isolated
workspaces for each, and runs a coding agent (Claude Code or Codex) to work on them
autonomously. It handles concurrency, retries, reconciliation, and observability so you can
point it at a Linear project and let agents work through your backlog.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [How It Works](#how-it-works)
- [Running Symphony](#running-symphony)
- [WORKFLOW.md Reference](#workflowmd-reference)
  - [File Format](#file-format)
  - [tracker](#tracker)
  - [polling](#polling)
  - [workspace](#workspace)
  - [hooks](#hooks)
  - [agent](#agent)
  - [codex](#codex)
  - [claude_code](#claude_code)
  - [server](#server)
- [Prompt Templates](#prompt-templates)
- [Workspace Management](#workspace-management)
- [Dispatch and Scheduling](#dispatch-and-scheduling)
- [Retries and Backoff](#retries-and-backoff)
- [Reconciliation](#reconciliation)
- [HTTP Server and API](#http-server-and-api)
- [Dynamic Reload](#dynamic-reload)
- [Environment Variables](#environment-variables)
- [Error Handling](#error-handling)
- [Operational Recipes](#operational-recipes)
- [Troubleshooting](#troubleshooting)

---

## Quick Start

### With Claude Code

```bash
# 1. Set your Linear API key
export LINEAR_API_KEY=lin_api_xxxxxxxx

# 2. Create a WORKFLOW.md
cat > WORKFLOW.md << 'EOF'
---
tracker:
  kind: linear
  project_slug: my-project
workspace:
  root: ~/symphony_workspaces
hooks:
  after_create: |
    git clone git@github.com:myorg/myrepo.git .
  before_run: |
    git fetch origin && git checkout main && git pull
agent:
  type: claude_code
  max_concurrent_agents: 3
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
  max_turns: 20
---
You are working on {{ issue.identifier }}: {{ issue.title }}.

{{ issue.description }}

Implement the requested changes, write tests, and create a pull request.
EOF

# 3. Run symphony
go run . WORKFLOW.md

# Or with the HTTP dashboard:
go run . --port 8080 WORKFLOW.md
```

### With Codex

```bash
# 1. Set your Linear API key
export LINEAR_API_KEY=lin_api_xxxxxxxx

# 2. Create a WORKFLOW.md
cat > WORKFLOW.md << 'EOF'
---
tracker:
  kind: linear
  project_slug: my-project
workspace:
  root: ~/symphony_workspaces
hooks:
  after_create: |
    git clone git@github.com:myorg/myrepo.git .
  before_run: |
    git fetch origin && git checkout main && git pull
agent:
  type: codex
  max_concurrent_agents: 3
codex:
  approval_policy: auto-edit
---
You are working on {{ issue.identifier }}: {{ issue.title }}.

{{ issue.description }}

Implement the requested changes, write tests, and create a pull request.
EOF

# 3. Run symphony
go run . WORKFLOW.md
```

---

## Installation

```bash
# Clone and build
git clone https://github.com/jordan/go-symphony.git
cd go-symphony
go build -o symphony .

# Or install directly
go install github.com/jordan/go-symphony@latest
```

**Requirements:**

- Go 1.22+
- A coding agent CLI installed and available in your PATH:
  - **Claude Code:** `claude` CLI ([install guide](https://docs.anthropic.com/en/docs/claude-code))
  - **Codex:** `codex` CLI (or configure a custom command)
- A Linear API key with read access to your project
- Git (if using git-based workspace hooks)

---

## How It Works

Symphony runs a continuous loop:

```
┌─────────────────────────────────────────────────────────┐
│                    Poll Tick (every 30s)                 │
│                                                         │
│  1. Reconcile running issues (stall + state check)      │
│  2. Validate config                                     │
│  3. Fetch candidate issues from Linear                  │
│  4. Sort by priority → age → identifier                 │
│  5. Dispatch eligible issues into isolated workspaces   │
└─────────────────────────────────────────────────────────┘
         │                              │
         ▼                              ▼
┌─────────────────┐          ┌─────────────────────┐
│  Worker (issue)  │          │  Worker (issue)      │
│                  │          │                      │
│  Create workspace│          │  Reuse workspace     │
│  Run hooks       │          │  Run hooks           │
│  Launch Codex    │          │  Launch Codex        │
│  Stream turns    │          │  Stream turns        │
│  Check state     │          │  Check state         │
│  Loop or exit    │          │  Loop or exit        │
└─────────────────┘          └─────────────────────┘
         │                              │
         ▼                              ▼
   Normal exit → continuation retry (1s)
   Error exit  → exponential backoff retry
```

Each issue gets its own workspace directory, its own Codex agent session, and its own retry
lifecycle. Symphony never runs agents in your main repository — everything happens in isolated
workspace directories.

---

## Running Symphony

```
symphony [flags] [path-to-WORKFLOW.md]
```

**Arguments:**

| Argument | Description | Default |
|----------|-------------|---------|
| `path-to-WORKFLOW.md` | Path to workflow file (positional) | `./WORKFLOW.md` |

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-port` | HTTP server port (overrides `server.port` in WORKFLOW.md) | disabled |

**Examples:**

```bash
# Use WORKFLOW.md in current directory
symphony

# Explicit workflow path
symphony /path/to/my/WORKFLOW.md

# With HTTP dashboard on port 8080
symphony --port 8080

# Both
symphony --port 8080 /path/to/WORKFLOW.md
```

**Signal handling:**

- `SIGINT` (Ctrl+C) and `SIGTERM` trigger graceful shutdown
- Running agent sessions are cancelled
- The process exits cleanly with code 0

---

## WORKFLOW.md Reference

### File Format

WORKFLOW.md is a Markdown file with optional YAML front matter. The front matter configures
Symphony's runtime behavior. The Markdown body is the prompt template sent to agents.

```markdown
---
# YAML front matter (configuration)
tracker:
  kind: linear
  project_slug: my-project
---
<!-- Markdown body (prompt template) -->
You are working on {{ issue.identifier }}: {{ issue.title }}.
```

**Parsing rules:**

- If the file starts with `---`, everything between the first and second `---` is YAML config
- Everything after the second `---` is the prompt template
- If there's no front matter, the entire file is treated as a prompt template with default config
- Front matter must be a YAML map (not a list or scalar)
- The prompt body is trimmed of leading/trailing whitespace

---

### `tracker`

Configures the issue tracker connection.

```yaml
tracker:
  kind: linear                          # Required. Only "linear" is supported
  endpoint: https://api.linear.app/graphql  # Linear GraphQL endpoint
  api_key: $LINEAR_API_KEY              # API key or $ENV_VAR reference
  project_slug: my-project              # Required. Linear project slug ID
  active_states:                        # States that trigger agent dispatch
    - Todo
    - In Progress
  terminal_states:                      # States that trigger cleanup
    - Done
    - Closed
    - Cancelled
    - Canceled
    - Duplicate
```

| Field | Type | Default | Required |
|-------|------|---------|----------|
| `kind` | string | — | Yes |
| `endpoint` | string | `https://api.linear.app/graphql` | No |
| `api_key` | string | `$LINEAR_API_KEY` | Yes (via config or env) |
| `project_slug` | string | — | Yes |
| `active_states` | list of strings | `["Todo", "In Progress"]` | No |
| `terminal_states` | list of strings | `["Done", "Closed", "Cancelled", "Canceled", "Duplicate"]` | No |

**Notes:**

- `api_key` supports `$VAR_NAME` syntax to read from environment variables
- If `api_key` is empty or unset for `kind: linear`, Symphony falls back to the `LINEAR_API_KEY`
  environment variable
- State matching is case-insensitive (`todo` matches `Todo`)
- `active_states` controls which issues get dispatched
- `terminal_states` controls when workspaces get cleaned up and running agents get stopped

---

### `polling`

Controls how often Symphony checks for new work.

```yaml
polling:
  interval_ms: 30000    # Poll every 30 seconds
```

| Field | Type | Default |
|-------|------|---------|
| `interval_ms` | integer | `30000` (30s) |

The poll interval can be changed dynamically by editing WORKFLOW.md — Symphony picks up the
new value on the next tick without restart.

---

### `workspace`

Controls where per-issue workspace directories are created.

```yaml
workspace:
  root: ~/symphony_workspaces
```

| Field | Type | Default |
|-------|------|---------|
| `root` | path string | `{system temp dir}/symphony_workspaces` |

**Path expansion:**

- `~` expands to the user's home directory
- `$VAR_NAME` resolves environment variables
- Relative paths are allowed but discouraged

Each issue gets a subdirectory named after its sanitized identifier:

```
~/symphony_workspaces/
  MT-123/        ← workspace for issue MT-123
  MT-456/        ← workspace for issue MT-456
  PROJ-789/      ← workspace for issue PROJ-789
```

---

### `hooks`

Shell scripts that run at specific points in the workspace lifecycle.

```yaml
hooks:
  after_create: |
    git clone git@github.com:myorg/myrepo.git .
    npm install
  before_run: |
    git fetch origin
    git checkout main
    git pull --ff-only
  after_run: |
    echo "Agent finished at $(date)" >> .symphony.log
  before_remove: |
    echo "Cleaning up workspace"
  timeout_ms: 120000
```

| Hook | When It Runs | Failure Behavior |
|------|-------------|------------------|
| `after_create` | Once, when a workspace directory is first created | **Fatal** — workspace is deleted, dispatch aborted |
| `before_run` | Before every agent session starts | **Fatal** — attempt aborted, triggers retry |
| `after_run` | After every agent session exits (success or failure) | **Non-fatal** — logged as warning |
| `before_remove` | Before a workspace directory is deleted | **Non-fatal** — logged as warning, deletion proceeds |

| Field | Type | Default |
|-------|------|---------|
| `timeout_ms` | integer | `60000` (60s) |

**Execution details:**

- All hooks run with `bash -lc` (login shell, so your PATH and profile are loaded)
- Working directory is set to the workspace path
- Hooks inherit Symphony's full environment
- Non-positive `timeout_ms` values fall back to the default (60s)
- `after_create` failure removes the partially-created workspace directory
- If `before_run` fails, `after_run` still runs (cleanup opportunity)

---

### `agent`

Controls which agent to use, concurrency, retries, and turn limits.

```yaml
agent:
  type: claude_code          # or "codex" (default)
  max_concurrent_agents: 5
  max_turns: 20
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    todo: 3
    in progress: 5
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `"codex"` | Agent backend: `"codex"` or `"claude_code"` |
| `max_concurrent_agents` | integer | `10` | Global limit on simultaneous agent sessions |
| `max_turns` | integer | `20` | Max agent turns per worker run before exiting |
| `max_retry_backoff_ms` | integer | `300000` (5m) | Cap on exponential backoff delay |
| `max_concurrent_agents_by_state` | map | `{}` | Per-state concurrency limits |

**Per-state concurrency:**

State keys are normalized to lowercase for lookup. For example, `todo: 3` means at most 3 agents
can work on "Todo" issues simultaneously. Issues in states without a per-state limit only
respect the global limit.

---

### `codex`

Configures the Codex app-server subprocess.

```yaml
codex:
  command: codex app-server
  approval_policy: auto-edit
  thread_sandbox: ""
  turn_sandbox_policy: ""
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `command` | string | `codex app-server` | Shell command to launch the agent |
| `approval_policy` | string | `""` | Codex approval policy for the session |
| `thread_sandbox` | string | `""` | Codex sandbox mode for the thread |
| `turn_sandbox_policy` | string | `""` | Codex sandbox policy per turn |
| `turn_timeout_ms` | integer | `3600000` (1h) | Max time for a single turn |
| `read_timeout_ms` | integer | `5000` (5s) | Timeout for reading individual messages |
| `stall_timeout_ms` | integer | `300000` (5m) | Kill session if no events for this long |

**Notes:**

- `command` is executed via `bash -lc` in the workspace directory
- Set `stall_timeout_ms` to `0` or negative to disable stall detection
- `approval_policy` values depend on your Codex version (e.g., `auto-edit`, `full-auto`)
- The agent process communicates over stdin/stdout using a JSON-RPC-like line protocol
- Approval requests from the agent are auto-approved
- User input requests cause the run to fail immediately (agents must work autonomously)

---

### `claude_code`

Configures the Claude Code CLI subprocess. Only used when `agent.type` is `"claude_code"`.

```yaml
claude_code:
  command: claude
  model: sonnet
  max_turns: 10
  permission_mode: auto
  allowed_tools:
    - Bash
    - Read
    - Edit
    - Write
    - Glob
    - Grep
  disallowed_tools: []
  append_system_prompt: "Always write tests for your changes."
  turn_timeout_ms: 3600000
  max_budget_usd: 5.00
  dangerously_skip_permissions: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `command` | string | `"claude"` | Claude Code CLI command |
| `model` | string | `""` | Model to use (`"opus"`, `"sonnet"`, `"haiku"`, or full model ID) |
| `max_turns` | integer | `0` (unlimited) | Max Claude Code turns per invocation |
| `allowed_tools` | list of strings | `[]` | Tools to auto-approve (e.g., `"Bash"`, `"Read"`, `"Edit"`) |
| `disallowed_tools` | list of strings | `[]` | Tools to block |
| `permission_mode` | string | `""` | Permission mode: `"default"`, `"plan"`, `"auto"` |
| `dangerously_skip_permissions` | bool | `false` | Auto-approve everything (use with extreme caution) |
| `append_system_prompt` | string | `""` | Additional system-level instructions |
| `turn_timeout_ms` | integer | `3600000` (1h) | Total timeout per invocation |
| `max_budget_usd` | float | `0` (unlimited) | Spending cap per invocation |

**How it works:**

Claude Code runs in non-interactive print mode (`claude -p "prompt"`). For each turn:

1. Symphony renders the prompt template with the issue data
2. Launches `claude -p "<prompt>" --output-format stream-json --verbose` in the workspace directory
3. Streams events back to the orchestrator for monitoring
4. On subsequent turns for the same issue, uses `--resume <session_id>` to continue the
   conversation

**Allowed tools example:**

You can use fine-grained tool permissions with wildcards:

```yaml
claude_code:
  allowed_tools:
    - Bash
    - Read
    - Edit
    - Write
    - "Bash(git *)"        # Only allow git commands
    - "Bash(npm test)"     # Only allow npm test
```

**Permission modes:**

| Mode | Behavior |
|------|----------|
| `default` | Claude asks for permission on most operations |
| `plan` | Claude proposes a plan and asks for approval before executing |
| `auto` | Claude auto-approves most operations (still respects allowed_tools) |
| (with `dangerously_skip_permissions: true`) | Everything auto-approved — no guardrails |

**Choosing between Claude Code and Codex:**

| Feature | Claude Code | Codex |
|---------|------------|-------|
| Protocol | CLI subprocess (`-p` flag) | JSON-RPC app-server over stdio |
| Session resume | `--resume <session_id>` | Same thread across turns |
| Permission model | `--allowedTools`, `--permission-mode` | `approvalPolicy`, `sandboxPolicy` |
| Streaming | `stream-json` output format | Line-delimited JSON events |
| Multi-turn | Each turn is a new `claude -p` invocation | Turns within a single process |
| Models | All Claude models | Codex-specific |

---

### `server`

Optional HTTP observability server.

```yaml
server:
  port: 8080
```

| Field | Type | Default |
|-------|------|---------|
| `port` | integer | disabled |

The CLI `-port` flag overrides this value. The server binds to `127.0.0.1` (loopback only).

---

## Prompt Templates

The Markdown body of WORKFLOW.md is a [Liquid](https://shopify.github.io/liquid/) template
rendered once per agent dispatch.

### Available Variables

**`issue`** — the normalized Linear issue:

| Field | Type | Example |
|-------|------|---------|
| `issue.id` | string | `"abc123def"` |
| `issue.identifier` | string | `"MT-123"` |
| `issue.title` | string | `"Fix login timeout"` |
| `issue.description` | string or nil | `"Users report..."` |
| `issue.priority` | integer or nil | `1` |
| `issue.state` | string | `"Todo"` |
| `issue.branch_name` | string or nil | `"mt-123-fix-login"` |
| `issue.url` | string or nil | `"https://linear.app/..."` |
| `issue.labels` | list of strings | `["bug", "urgent"]` |
| `issue.blocked_by` | list of blockers | see below |
| `issue.created_at` | string or nil | `"2026-01-15T10:30:00Z"` |
| `issue.updated_at` | string or nil | `"2026-03-10T14:22:00Z"` |

Each blocker in `issue.blocked_by` has: `id`, `identifier`, `state`.

**`attempt`** — retry/continuation metadata:

- `nil` on first run
- Integer (1+) on retry or continuation

### Example Template

```liquid
You are an autonomous coding agent working on {{ issue.identifier }}: {{ issue.title }}.

{% if issue.description %}
## Issue Description
{{ issue.description }}
{% endif %}

{% if issue.labels.size > 0 %}
**Labels:** {% for label in issue.labels %}{{ label }}{% unless forloop.last %}, {% endunless %}{% endfor %}
{% endif %}

{% if issue.blocked_by.size > 0 %}
**Note:** This issue has blockers:
{% for b in issue.blocked_by %}
- {{ b.identifier }} ({{ b.state }})
{% endfor %}
{% endif %}

{% if attempt %}
This is retry attempt {{ attempt }}. Review your previous work and continue.
{% endif %}

## Instructions
1. Read and understand the requirements
2. Implement the changes
3. Write tests
4. Create a pull request
5. Move the issue to "Human Review"
```

### Strict Rendering

Template rendering uses strict mode:

- Unknown variables cause the render to fail (typo protection)
- Unknown filters cause the render to fail
- A render failure aborts that run attempt and triggers a retry

If the template body is empty, Symphony uses a minimal fallback prompt:
`"You are working on an issue from Linear."`

---

## Workspace Management

### Directory Structure

```
{workspace.root}/
  {sanitized-identifier}/     ← one per issue
```

The identifier is sanitized: any character not in `[A-Za-z0-9._-]` is replaced with `_`.

| Identifier | Workspace Directory |
|------------|-------------------|
| `MT-123` | `MT-123/` |
| `PROJ-456` | `PROJ-456/` |
| `feat/login` | `feat_login/` |

### Lifecycle

1. **First dispatch:** Directory created → `after_create` hook runs → `before_run` hook runs →
   agent session starts
2. **Subsequent dispatches (same issue):** Directory reused → `before_run` hook runs → agent
   session starts
3. **Agent exits:** `after_run` hook runs (always, even on failure)
4. **Issue reaches terminal state:** `before_remove` hook runs → directory deleted

### Safety Invariants

- Workspace paths are validated to be under the configured workspace root (no path traversal)
- The agent always runs with its working directory set to the workspace path
- Workspace directory names use only safe characters

---

## Dispatch and Scheduling

### Eligibility Rules

An issue is dispatched only when **all** of these are true:

1. It has `id`, `identifier`, `title`, and `state` fields
2. Its state is in `active_states`
3. Its state is **not** in `terminal_states`
4. It is not already running or claimed by another worker
5. Global concurrency slots are available
6. Per-state concurrency slots are available (if configured)
7. **Blocker rule:** If the issue state is "Todo", all blockers must be in terminal states

### Priority Order

Issues are dispatched in this order:

1. **Priority** — lower number = higher priority (1 before 2 before 3); null priority sorts last
2. **Created date** — older issues first
3. **Identifier** — lexicographic tiebreaker (`A-1` before `B-1`)

### Concurrency

Global concurrency is controlled by `agent.max_concurrent_agents` (default: 10).

Per-state concurrency is controlled by `agent.max_concurrent_agents_by_state`. For example:

```yaml
agent:
  max_concurrent_agents: 10
  max_concurrent_agents_by_state:
    todo: 3         # At most 3 Todo issues at once
    in progress: 8  # At most 8 In Progress issues at once
```

---

## Retries and Backoff

### Normal Completion

When an agent session completes normally (all turns done or issue state changed), Symphony
schedules a **continuation retry** after 1 second. This re-checks whether the issue is still
active and needs more work.

### Error Retries

When an agent session fails (hook error, agent error, timeout, stall), Symphony schedules a
retry with exponential backoff:

```
delay = min(10s × 2^(attempt-1), max_retry_backoff_ms)
```

| Attempt | Delay |
|---------|-------|
| 1 | 10s |
| 2 | 20s |
| 3 | 40s |
| 4 | 80s |
| 5 | 160s |
| 6+ | 300s (5m cap) |

The cap is configurable via `agent.max_retry_backoff_ms`.

### Retry Behavior

When a retry timer fires:

1. Fetch current candidate issues from Linear
2. Find the issue by ID
3. If **not found** or no longer active → release claim (stop retrying)
4. If found and eligible → dispatch if slots available
5. If found but **no slots** → reschedule with incremented attempt

---

## Reconciliation

Reconciliation runs at the start of every poll tick to keep orchestrator state in sync with
Linear.

### Stall Detection

If `codex.stall_timeout_ms > 0` (default: 5 minutes), Symphony checks each running session
for inactivity. If no events have been received for longer than the threshold, the session is
terminated and a retry is scheduled.

### State Refresh

Symphony fetches the current state of all running issues from Linear:

| Linear State | Action |
|-------------|--------|
| Still active (e.g., "In Progress") | Update local state, keep running |
| Terminal (e.g., "Done", "Closed") | Stop agent, clean workspace, release claim |
| Neither active nor terminal | Stop agent (no workspace cleanup), release claim |

If the Linear API call fails, Symphony keeps all workers running and retries next tick.

### Startup Cleanup

On startup, Symphony queries Linear for all issues in terminal states and removes any
leftover workspace directories. This prevents stale workspaces from accumulating across
restarts.

---

## HTTP Server and API

Enable the HTTP server with `-port` or `server.port` in WORKFLOW.md. The server binds to
`127.0.0.1` (localhost only).

### Dashboard — `GET /`

An HTML dashboard that auto-refreshes every 5 seconds. Shows:

- Running sessions (issue, state, session ID, turn count, last event, tokens)
- Retrying issues (issue, attempt number, due time, error)
- Aggregate totals (tokens consumed, total runtime)

### JSON API

#### `GET /api/v1/state`

Full state snapshot.

```json
{
  "generated_at": "2026-03-13T12:00:00Z",
  "counts": {
    "running": 2,
    "retrying": 1
  },
  "running": [
    {
      "issue_id": "abc123",
      "issue_identifier": "MT-123",
      "state": "In Progress",
      "session_id": "thread-1-turn-1",
      "turn_count": 5,
      "last_event": "notification",
      "last_message": "Running tests...",
      "started_at": "2026-03-13T11:55:00Z",
      "last_event_at": "2026-03-13T11:59:45Z",
      "tokens": {
        "input_tokens": 12000,
        "output_tokens": 5000,
        "total_tokens": 17000
      }
    }
  ],
  "retrying": [
    {
      "issue_id": "def456",
      "issue_identifier": "MT-456",
      "attempt": 3,
      "due_at": "2026-03-13T12:05:00Z",
      "error": "turn_timeout"
    }
  ],
  "codex_totals": {
    "input_tokens": 50000,
    "output_tokens": 24000,
    "total_tokens": 74000,
    "seconds_running": 1834.2
  },
  "rate_limits": null
}
```

#### `GET /api/v1/{identifier}`

Status for a specific issue (e.g., `/api/v1/MT-123`). Returns 404 if the issue isn't in the
current in-memory state.

#### `POST /api/v1/refresh`

Triggers an immediate poll + reconciliation cycle. Returns `202 Accepted`:

```json
{
  "queued": true,
  "coalesced": false,
  "requested_at": "2026-03-13T12:00:00Z",
  "operations": ["poll", "reconcile"]
}
```

---

## Dynamic Reload

Symphony watches WORKFLOW.md for changes using filesystem notifications. When you edit the
file:

1. Changes are debounced (200ms) to handle partial writes
2. The file is re-parsed
3. If parsing succeeds, config and prompt template are updated atomically
4. If parsing fails, the last known good config is kept and an error is logged

**What updates immediately (next tick):**

- Poll interval
- Concurrency limits
- Active/terminal state lists
- Workspace root and hooks
- Codex command, timeouts, and policies
- Prompt template (for new dispatches)
- Linear endpoint/auth/project

**What doesn't update:**

- Already-running agent sessions continue with their original config
- The HTTP server port (requires restart)

---

## Environment Variables

| Variable | Purpose | Required |
|----------|---------|----------|
| `LINEAR_API_KEY` | Linear API authentication token | Yes (unless set in `tracker.api_key`) |

You can also reference any environment variable in WORKFLOW.md using `$VAR_NAME` syntax:

```yaml
tracker:
  api_key: $MY_CUSTOM_LINEAR_KEY

workspace:
  root: $SYMPHONY_WORKSPACE_DIR
```

---

## Error Handling

### Startup Errors

These prevent Symphony from starting:

- Missing or unreadable WORKFLOW.md
- Invalid YAML front matter
- Missing `tracker.kind`, `tracker.api_key`, or `tracker.project_slug`
- Unsupported `tracker.kind` (anything other than `linear`)
- Empty `codex.command`

### Runtime Errors

These are handled gracefully (Symphony keeps running):

| Error | Behavior |
|-------|----------|
| Linear API failure (polling) | Skip dispatch this tick, retry next tick |
| Linear API failure (reconciliation) | Keep workers running, retry next tick |
| Workspace creation failure | Retry with backoff |
| Hook failure (`after_create`) | Workspace removed, retry with backoff |
| Hook failure (`before_run`) | Attempt aborted, retry with backoff |
| Hook failure (`after_run`, `before_remove`) | Logged and ignored |
| Agent startup failure | Retry with backoff |
| Agent turn failure | Retry with backoff |
| Agent turn timeout | Retry with backoff |
| Agent stall detected | Session killed, retry with backoff |
| User input requested by agent | Run fails immediately, retry with backoff |
| Workflow reload failure | Last good config kept, error logged |

---

## Operational Recipes

### Stop an agent working on a specific issue

Move the issue to a terminal state in Linear (e.g., "Cancelled"). Symphony's reconciliation
will detect this within one poll interval and stop the agent.

### Force an immediate check

```bash
curl -X POST http://localhost:8080/api/v1/refresh
```

### Check what's running

```bash
# All state
curl http://localhost:8080/api/v1/state | jq

# Specific issue
curl http://localhost:8080/api/v1/MT-123 | jq
```

### Limit how many Todo issues run concurrently

```yaml
agent:
  max_concurrent_agents_by_state:
    todo: 2
```

### Switch from Codex to Claude Code

```yaml
agent:
  type: claude_code
claude_code:
  model: opus
  permission_mode: auto
  allowed_tools:
    - Bash
    - Read
    - Edit
    - Write
```

### Use Claude Code with a spending cap

```yaml
agent:
  type: claude_code
claude_code:
  model: sonnet
  max_budget_usd: 2.00
  max_turns: 10
```

### Use Claude Code with restricted bash access

```yaml
agent:
  type: claude_code
claude_code:
  allowed_tools:
    - Read
    - Edit
    - Write
    - Glob
    - Grep
    - "Bash(git *)"
    - "Bash(npm test)"
    - "Bash(npm run build)"
```

### Use a custom Codex build

```yaml
codex:
  command: /path/to/my/codex app-server
```

### Run with a longer stall timeout

```yaml
codex:
  stall_timeout_ms: 600000   # 10 minutes
```

### Disable stall detection

```yaml
codex:
  stall_timeout_ms: 0
```

### Use different workspace roots per environment

```yaml
workspace:
  root: $SYMPHONY_WORKSPACE_ROOT
```

```bash
SYMPHONY_WORKSPACE_ROOT=/mnt/fast-ssd/workspaces symphony WORKFLOW.md
```

---

## Troubleshooting

### Symphony exits immediately with a config error

Check that your WORKFLOW.md has valid YAML front matter with `tracker.kind: linear`,
`tracker.project_slug`, and that `LINEAR_API_KEY` is set in your environment.

### No issues are being dispatched

- Check that your project slug matches the Linear project's slug ID (not the display name)
- Verify your issues are in one of the `active_states` (default: "Todo" or "In Progress")
- Check that issues aren't blocked by non-terminal issues (blocker rule for "Todo")
- Check concurrency limits — you may be at capacity

### Agent sessions keep failing

- Check that the `codex.command` is available in your PATH (try running it manually)
- Look at the structured log output for specific error messages
- Try running with a longer `codex.stall_timeout_ms` if sessions are being killed prematurely

### Workspaces aren't being created correctly

- Verify the `hooks.after_create` script works when run manually in an empty directory
- Check the `hooks.timeout_ms` — clone operations on large repos may need more time
- Check filesystem permissions on the workspace root directory

### Dynamic reload isn't working

- Symphony watches for filesystem write events — make sure you're saving the file (not just
  editing in a buffer)
- Check the log output for "workflow reloaded" or "workflow reload failed" messages
- Some editors write to a temp file and rename — this should still work via the `Create` event

### Dashboard shows stale data

- The HTML dashboard auto-refreshes every 5 seconds
- The JSON API (`/api/v1/state`) always returns live data
- Use `POST /api/v1/refresh` to trigger an immediate reconciliation

### Agent auto-approves everything

This is by design. Symphony auto-approves all approval requests from the agent and fails
immediately on user-input requests. The agents are expected to work autonomously. Configure
the `codex.approval_policy` and sandbox settings to control what the agent is allowed to do.
