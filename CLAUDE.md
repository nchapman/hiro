# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Hive?

Hive is a distributed AI agent platform written in Go. A single binary serves an HTTP API, a WebSocket chat endpoint, and an embedded React dashboard. Agents are defined as markdown files with YAML frontmatter; they run agentic loops backed by [charm.land/fantasy](https://charm.land/fantasy) and can spawn/manage child agents.

## Build & Dev Commands

```bash
make build        # Build web UI + Go binary (outputs ./hive)
make build-dev    # Build Go binary without web UI (uses -tags dev)
make test         # go test ./... -v -count=1
make check        # test + go vet
make web          # Build web UI only (cd web/ui && npm install && npm run build)
make docker       # Docker build
```

Run a single test:
```bash
go test ./internal/history/... -run TestCompaction -v -count=1
```

Web UI dev server (separate terminal):
```bash
cd web/ui && npm run dev
```

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `HIVE_API_KEY` | *(none)* | LLM provider API key (required for agents) |
| `HIVE_PROVIDER` | `anthropic` | LLM provider (`anthropic` or `openrouter`) |
| `HIVE_MODEL` | *(from agent config)* | Override model for all agents |
| `HIVE_ADDR` | `:8080` | HTTP listen address |
| `HIVE_WORKSPACE_DIR` | `.` | Root containing `agents/` and `instances/` |
| `HIVE_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |

A `.env` file is loaded automatically via godotenv (does not override existing vars).

## Architecture

### Agent Lifecycle

```
agents/<name>/agent.md  →  config.LoadAgentDir()  →  agent.New()  →  Manager tracks instance
                                                                       ↓
                                                              instances/<uuid>/
                                                                manifest.json
                                                                memory.md
                                                                identity.md
                                                                todos.json
                                                                history.db
```

- **Agent definitions** live in `agents/<name>/` with `agent.md` (required), optional `soul.md`, `tools.md`, and a `skills/` subdirectory.
- **Instances** are runtime state stored in `instances/<uuid>/`. Persistent agents survive restarts via `RestoreInstances()`.
- **Ephemeral agents** are spawned via `spawn_agent` tool, run a single prompt, and are cleaned up.
- **Persistent agents** get extra tools: `memory_read/write`, `todos`, `history_search/recall`.

### Agent Definition Structure

```
agents/<name>/
  agent.md          # Required. YAML frontmatter (name, model, mode, description) + markdown body (system prompt)
  soul.md           # Optional. Persona, tone, boundaries — prepended to system prompt
  tools.md          # Optional. Tool usage guidelines — appended as "## Tool Notes"
  skills/
    flat-skill.md           # Flat file skill
    dir-skill/
      SKILL.md              # Directory skill (can bundle scripts/, references/, assets/)
      scripts/
      references/
      assets/
```

A workspace-level `skills/` directory provides shared skills available to all agents. Agent-specific skills take precedence over shared skills with the same name.

Skills use **progressive disclosure** — only name and description are listed in the system prompt. The agent activates a skill via the `use_skill` tool, which returns the full instructions and lists bundled resources.

### Skill File Format

```yaml
---
name: skill-name          # Required. Lowercase kebab-case, max 64 chars.
description: What and when. # Required. Max 1024 chars. Trigger mechanism for the agent.
license: MIT               # Optional.
compatibility: Requires X  # Optional. Max 500 chars.
metadata:                  # Optional. Arbitrary key-value pairs.
  author: name
  version: "1.0"
---

Full instructions (read on demand by the agent).
```

Validation: name must match `^[a-z0-9]+(-[a-z0-9]+)*$`. For directory skills, name must match directory name (case-insensitive).

### System Prompt Assembly Order

Each turn, `currentSystemPrompt()` rebuilds the full prompt from disk:

1. `soul.md` content (if present)
2. `## Identity` + `identity.md` (persistent agents only)
3. `## Memories` + `memory.md` (persistent agents only)
4. `## Current Tasks` + formatted todos (persistent agents only)
5. `agent.md` body (main instructions)
6. `## Tool Notes` + `tools.md` (if present)
7. `## Skills` + XML listing of skill name/description/path (if present)

Skills are re-scanned from disk each turn (like memory and identity), so runtime-created skills take effect immediately. The full skill body is NOT in the prompt — agents read it on demand via `read_file`.

### Key Packages

- **`cmd/hive`** — Entry point. Loads config, starts manager, boots coordinator agent, runs HTTP server.
- **`internal/agent`** — Agent runtime. `Agent` wraps a `fantasy.Agent`; `Manager` supervises lifecycles, provides manager tools, and serializes per-agent conversations.
- **`internal/agent/tools/`** — All built-in tool implementations (read_file, write_file, edit, multiedit, bash, job_output, job_kill, glob, grep, list_files, fetch).
- **`internal/config`** — Markdown+YAML parsing, agent/skill config loading, manifest/memory/todos persistence.
- **`internal/history`** — SQLite-backed conversation history with automatic LLM-driven compaction. `Engine` coordinates `Store` (persistence) + `Compactor` (summarization) + `Assemble` (context assembly within token budget).
- **`internal/hub`** — Swarm management: tracks connected workers and dispatches tasks by skill.
- **`internal/transport`** — Wire protocol (WebSocket JSON envelopes) for leader↔worker communication.
- **`internal/api`** — HTTP server with REST endpoints (`/api/health`, `/api/agents`, `/api/agents/{id}/messages`) and WebSocket chat (`/ws/chat`).
- **`web/`** — Embedded React UI (Vite + TypeScript). Built assets in `web/ui/dist/` are embedded via `//go:embed`.

## Agent Tools

### Built-in Tools (all agents get these 11)

Defined in `internal/agent/tools.go` via `buildTools()`. Implementations in `internal/agent/tools/*.go`.

| Tool | Purpose | Key Params | Constraints |
|------|---------|------------|-------------|
| `read_file` | Read file contents with line numbers | `path`, `offset`, `limit` | 64KB max output |
| `write_file` | Write full content to file (creates dirs) | `path`, `content` | Full replacement only |
| `edit` | Surgical find-and-replace edits | `file_path`, `old_string`, `new_string`, `replace_all` | Single match must be unique; empty `old_string` + content = create file |
| `multiedit` | Batch multiple edits to one file | `file_path`, `edits` (array of `{old_string, new_string, replace_all}`) | Edits applied sequentially; partial success supported |
| `list_files` | List directory contents | `path`, `pattern` (glob) | Max 500 entries; skips node_modules, vendor, dist, .git, hidden dirs |
| `glob` | Find files by glob pattern | `pattern`, `path` | Max 100 results; uses ripgrep if available, falls back to Go; sorted by mod time (newest first) |
| `grep` | Search file contents with regex | `pattern`, `path`, `include` (file glob), `literal_text` | Max 100 matches; 30s timeout; uses ripgrep if available |
| `bash` | Execute shell commands | `command`, `working_dir`, `run_in_background` | 120s timeout (sync), 32KB max output; auto-backgrounds after 60s |
| `job_output` | Get output from background job | `job_id`, `wait` | Returns stdout/stderr and completion status |
| `job_kill` | Terminate a background job | `job_id` | Immediately terminates the process |
| `fetch` | Fetch URL content | `url` | 30s timeout, 64KB max response; runs in parallel |

### Manager Tools (agents that can have children)

Defined in `internal/agent/tools_manager.go` via `buildManagerTools(parentID)`. All scoped to calling agent's descendants via `IsDescendant()`.

| Tool | Purpose | Key Params | Behavior |
|------|---------|------------|----------|
| `spawn_agent` | Run ephemeral subagent to completion | `agent` (name), `prompt` | Blocks until done; forces ephemeral mode; cleans up after; 32KB max result |
| `start_agent` | Start a persistent child agent | `agent` (name) | Returns agent ID; respects agent config mode |
| `send_message` | Send message to child and get response | `agent_id`, `message` | Blocks; scoped to descendants; serialized per-agent (mutex); 32KB max result |
| `stop_agent` | Stop agent and its subtree | `agent_id` | Stops leaf-first; cleans up ephemeral dirs; persists persistent agents |
| `list_agents` | List direct child agents | *(none)* | Shows name, ID, mode, description for direct children only |

### Persistent Agent Tools (mode: persistent only)

Added in `startInstance()` via `buildMemoryTools()`, `buildTodoTools()`, `buildHistoryTools()`.

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `memory_read` | Read `memory.md` from instance dir | *(none)* | Returns empty if no memories yet |
| `memory_write` | Overwrite `memory.md` | `content` | Full replacement — read first to avoid data loss; 0600 perms; visible in system prompt next turn |
| `todos` | Manage task list | `todos` (array of `{content, status, active_form}`) | Full replacement; statuses: pending, in_progress, completed |
| `history_search` | Full-text search conversation history | `query`, `scope` (messages\|summaries\|all) | Max 20 results via SQLite FTS; only if history engine initialized |
| `history_recall` | Expand a summary's details | `summary_id` | Shows full text + children; depth, compression ratio, time range |

### Skill Tool (agents with skills)

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `use_skill` | Activate a skill and get full instructions | `name` | Returns full skill body + directory listing of bundled resources. Only present when agent has skills available. |

### Tool Totals by Agent Type

- **Ephemeral agents:** 11 built-in + 5 manager = 16 tools (+ 1 if skills)
- **Persistent agents:** 11 built-in + 5 manager + 2 memory + 1 todos + 2 history = 21 tools (+ 1 if skills)

## Coordinator Agent

The coordinator (`agents/coordinator/agent.md`) is the top-level agent. It is a persistent agent with model `claude-sonnet-4-20250514`.

**Bootstrap flow** (`cmd/hive/main.go`):
1. Check `HIVE_API_KEY` is set
2. Create `Manager` with provider/API key
3. `RestoreInstances()` — resume any persistent agents from prior runs
4. `AgentByName("coordinator")` — check if already running (from restore)
5. If not running, `StartAgent(ctx, "coordinator", "")` — no parent, becomes root

The coordinator has **no special code paths** — it's a regular persistent agent that happens to be started first. It gets all 18 tools.

## Creating Agents at Runtime

Agents can create new agent definitions at runtime using their file tools:
1. Use `write_file` / `edit` to create `agents/<name>/agent.md` (and optionally `soul.md`, `tools.md`, `skills/*.md`)
2. Call `start_agent` with the new agent name — `LoadAgentDir()` is called fresh each time, so it picks up the new definition immediately
3. No restart or reload mechanism needed

Similarly, skills can be added by writing `.md` files to an agent's `skills/` directory (flat or directory format). Skills are re-scanned from disk each turn, so new skills take effect on the next `StreamChat` call.

## Conversation Modes

- **Persistent agents** use `history.Engine` — messages are stored in SQLite, automatically compacted via LLM summarization, and assembled within a token budget.
- **Ephemeral agents** keep messages in-memory only (discarded on stop).
- **WebSocket chat** creates per-connection conversations for ephemeral agents, or uses shared persistent history for persistent agents.

### Agent Tool Scoping

Manager tools are scoped to the calling agent's descendants via `IsDescendant()`. An agent cannot manage siblings or ancestors.

## Testing Notes

- Tests use `fantasy.LanguageModel` injection (`opts.LM`) to avoid real API calls.
- `history` tests use `NewEngineWithSummarizer()` with a mock summarizer.
- The `tools/` package tests run actual file/process operations in temp directories.
- CGO is not required — SQLite uses `modernc.org/sqlite` (pure Go).
- No sandbox — agents can access the full filesystem and run any bash command.
