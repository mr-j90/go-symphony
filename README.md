# go-symphony
<img width="1536" height="1024" alt="image" src="https://github.com/user-attachments/assets/c0a65457-12ab-430e-bb6b-4e18e8087f68" />

A daemon that turns your Linear backlog into autonomous coding sessions. Symphony polls Linear for issues, creates isolated workspaces, and dispatches coding agents (Claude Code or Codex) to work on them — handling concurrency, retries, state transitions, and observability automatically.

## How It Works

```
Linear (Todo)  →  Symphony picks up issue
               →  Moves to "In Progress"
               →  Creates workspace + clones repo
               →  Launches coding agent
               →  Agent implements changes, writes tests, opens PR
               →  Issue moves to "Human Review"
```

Symphony runs as a long-lived process with a 30-second poll loop. Each issue gets its own workspace directory and agent session. Failed runs retry with exponential backoff. Issues that move to terminal states in Linear automatically stop their agent and clean up.

## Quick Start

```bash
# 1. Install
go install github.com/jordan/go-symphony@latest
# or: git clone && go build -o symphony .

# 2. Set your Linear API key
export LINEAR_API_KEY=lin_api_xxxxxxxx

# 3. Create a WORKFLOW.md (see Configuration below)

# 4. Run
symphony WORKFLOW.md

# With the web dashboard:
symphony --port 8080 WORKFLOW.md
```

## Requirements

- Go 1.22+
- A Linear API key ([Settings → API](https://linear.app/settings/api))
- One of:
  - [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI (`claude`)
  - [Codex](https://github.com/openai/codex) CLI (`codex`)
- Git (for workspace hooks)

## Configuration

All configuration lives in a single `WORKFLOW.md` file — a Markdown file with YAML front matter for settings and a Liquid template body for the agent prompt.

### Minimal Example (Claude Code)

```yaml
---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: my-project-abc123    # from your Linear project URL
  active_states:
    - Todo
    - In Progress

workspace:
  root: ~/symphony_workspaces

hooks:
  after_create: |
    git clone git@github.com:myorg/myrepo.git .
  before_run: |
    git fetch origin && git checkout main && git pull

agent:
  type: claude_code
  dispatch_transition_state: In Progress
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
  max_turns: 10
---
You are working on {{ issue.identifier }}: {{ issue.title }}.

{{ issue.description }}

Implement the changes, write tests, and create a pull request.
```

### Finding Your Project Slug

The `project_slug` is the `slugId` from your Linear project URL:

```
https://linear.app/myteam/project/my-project-abc123def456/overview
                                  ^^^^^^^^^^^^^^^^^^^^^^^^
                                  this is the project_slug
```

### Configuration Reference

| Section | Key Fields |
|---------|-----------|
| **tracker** | `kind` (linear), `api_key`, `project_slug`, `active_states`, `terminal_states` |
| **polling** | `interval_ms` (default: 30000) |
| **workspace** | `root` (supports `~` and `$ENV_VAR`) |
| **hooks** | `after_create`, `before_run`, `after_run`, `before_remove`, `timeout_ms` |
| **agent** | `type` (codex/claude_code), `dispatch_transition_state`, `max_concurrent_agents`, `max_turns` |
| **claude_code** | `model`, `permission_mode`, `allowed_tools`, `max_turns`, `max_budget_usd` |
| **codex** | `command`, `approval_policy`, `turn_timeout_ms`, `stall_timeout_ms` |
| **server** | `port` (enables HTTP dashboard) |

See [docs/USER_GUIDE.md](docs/USER_GUIDE.md) for the complete reference.

## Agent Backends

### Claude Code

```yaml
agent:
  type: claude_code
claude_code:
  model: sonnet              # opus, sonnet, haiku
  permission_mode: auto      # default, plan, auto
  allowed_tools:             # fine-grained tool control
    - Bash
    - Read
    - Edit
    - Write
  max_turns: 10
  max_budget_usd: 5.00       # spending cap per run
```

Runs `claude -p` in non-interactive mode. Supports session resume across turns via `--resume`.

### Codex

```yaml
agent:
  type: codex
codex:
  command: codex app-server
  approval_policy: auto-edit
  turn_timeout_ms: 3600000
```

Communicates via JSON-RPC over stdio with the Codex app-server protocol.

## Dispatch and Scheduling

- **Priority ordering**: lower number = higher priority, then oldest first, then identifier tiebreaker
- **Blocker rule**: "Todo" issues blocked by non-terminal issues are skipped
- **Concurrency**: global limit + optional per-state limits
- **State transitions**: optionally auto-move issues on dispatch (e.g., Todo → In Progress)

## Retry Behavior

| Outcome | Retry Delay |
|---------|-------------|
| Normal completion | 1 second (re-checks if issue still needs work) |
| Error (attempt 1) | 10 seconds |
| Error (attempt 2) | 20 seconds |
| Error (attempt 3) | 40 seconds |
| Error (attempt N) | min(10s × 2^(N-1), max_retry_backoff_ms) |

## Workspace Lifecycle

```
1. Issue dispatched
2. mkdir <workspace_root>/<issue-identifier>/
3. Run after_create hook (clone repo, install deps)  ← only on first create
4. Run before_run hook (git pull, sync)               ← every attempt
5. Launch agent
6. Run after_run hook                                 ← every attempt (even on failure)
7. Issue goes terminal → Run before_remove hook → rm -rf workspace
```

Workspaces persist across retries and continuation runs. Cleanup happens when issues reach terminal states.

## Observability

### HTTP Dashboard

Enable with `--port 8080` or `server.port` in WORKFLOW.md.

- **`GET /`** — HTML dashboard (auto-refreshes every 5s)
- **`GET /api/v1/state`** — JSON snapshot of all running/retrying sessions, token totals
- **`GET /api/v1/{identifier}`** — Status for a specific issue (e.g., `/api/v1/ZYX-41`)
- **`POST /api/v1/refresh`** — Trigger immediate poll + reconciliation

### Structured Logs

All logs include `issue_id`, `issue_identifier`, and `session_id` for filtering.

## Dynamic Reload

Edit `WORKFLOW.md` while Symphony is running — changes are picked up automatically within 200ms:

- Poll interval, concurrency limits, state lists
- Agent config (model, tools, timeouts)
- Prompt template
- Hooks and workspace settings

In-flight sessions continue with their original config. New dispatches use the updated config.

## Reconciliation

Every poll tick, Symphony:

1. **Stall detection** — kills sessions with no activity for `stall_timeout_ms`
2. **State refresh** — checks Linear for state changes on all running issues
   - Terminal state → stop agent, clean workspace
   - Still active → keep running
   - Non-active, non-terminal → stop agent (no cleanup)

## Operator Controls

- **Stop an agent**: move the issue to a terminal state in Linear (Done, Cancelled, etc.)
- **Force refresh**: `curl -X POST http://localhost:8080/api/v1/refresh`
- **Change behavior**: edit WORKFLOW.md (auto-reloads)
- **Restart**: Ctrl+C and re-run (recovers via fresh poll)

## Project Structure

```
go-symphony/
├── main.go                          # CLI entrypoint
├── WORKFLOW.md                      # Your workflow config + prompt
├── docs/USER_GUIDE.md               # Complete reference docs
├── internal/
│   ├── model/types.go               # Domain types
│   ├── config/config.go             # Typed config with defaults + env resolution
│   ├── workflow/
│   │   ├── loader.go                # WORKFLOW.md parser
│   │   ├── renderer.go              # Liquid template engine
│   │   └── watcher.go               # Filesystem watch for live reload
│   ├── linear/client.go             # Linear GraphQL client
│   ├── workspace/manager.go         # Per-issue workspace lifecycle
│   ├── agent/
│   │   ├── runner.go                # Codex app-server integration
│   │   └── claude.go                # Claude Code CLI integration
│   ├── orchestrator/orchestrator.go # Poll loop, dispatch, reconciliation
│   └── server/server.go             # HTTP dashboard + JSON API
```

## License

MIT
