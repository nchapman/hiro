# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Hive?

Hive is a distributed AI agent platform written in Go. A single binary serves an HTTP API, a WebSocket chat endpoint, and an embedded React dashboard. Agents are defined as markdown files with YAML frontmatter; they run agentic loops backed by [charm.land/fantasy](https://charm.land/fantasy) and can spawn/manage child agents.

## Build & Dev Commands

```bash
make build        # Build web UI + Go binary (outputs ./hive)
make build-dev    # Build Go binary without web UI (uses -tags dev)
make test            # Run tests in Docker (builds test container)
make test-local      # Run tests locally (no Docker, uses mock workers)
make test-isolation  # Run UID isolation tests in Docker (requires user pool)
make check           # test + go vet (in Docker)
make web          # Build web UI only (cd web/ui && npm install && npm run build)
make docker       # Docker build
make proto        # Regenerate protobuf (requires protoc)
```

Run a single test:
```bash
go test ./internal/platform/db/... -run TestCompaction -v -count=1
```

Run integration tests that hit real APIs (excluded from normal `make test`):
```bash
go test ./internal/agent/... -tags=online -v -count=1
```

Web UI dev server (separate terminal — proxies `/api` and `/ws` to `localhost:8080`):
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
| `HIVE_ROOT` | `.` | Platform root containing `agents/`, `sessions/`, `skills/`, `workspace/` |
| `HIVE_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |

A `.env` file is loaded automatically via godotenv (does not override existing vars).

## Architecture

### Process Model

Hive uses **process isolation**: the control plane owns all inference (LLM calls, conversation history, system prompt assembly) while tool execution runs in separate worker processes under isolated Unix UIDs. Communication is via gRPC over Unix sockets.

```
Control Plane Process (hive)
├── HTTP/WS API (web UI, REST, chat)
├── Inference loops (fantasy agent per session)
├── Unified database (db/hive.db — sessions, messages, usage)
├── System prompt assembly, context management, compaction
├── Local tools: memory, todos, history, spawn, coordinator, skills
├── Secrets + tool policies (config.yaml)
├── Process registry + session lifecycle
└── Spawns: hive agent (one worker per session)

Agent Worker Process (hive agent)
├── Tool execution sandbox (bash, file ops, glob, grep, fetch)
├── gRPC AgentWorker server (ExecuteTool + Shutdown only)
└── Runs under isolated UID for security
```

**Spawn protocol**: Control plane spawns `hive agent`, pipes `SpawnConfig` as JSON to stdin. Worker starts a gRPC server on a Unix socket and writes "ready" to stdout. Control plane connects and dispatches tool calls via `ExecuteTool` RPC. When UID isolation is enabled, each worker runs as a dedicated Unix user via `SysProcAttr.Credential`.

**Unix user isolation**: Auto-detected at startup (enabled iff `hive-agents` group exists). A pre-created pool of 64 Unix users (`hive-agent-0` through `hive-agent-63`, UIDs 10000-10063) provides per-agent isolation. Session dirs are `chown`ed to the agent's UID. Workspace uses setgid (`2775`) for collaborative file access. The control plane runs as root inside Docker for UID switching. `config.yaml` is `0600` root-owned, unreadable by agents.

**Group-based access control**: Two Unix groups control filesystem access:
- `hive-agents` (GID 10000) — primary group for all agent UIDs. Grants read/write to `/hive` and `/opt/mise`.
- `hive-coordinators` (GID 10001) — supplementary group for coordinator-mode agents. Grants write access to `agents/` and `skills/` directories (setgid `2775`). Non-coordinator agents get read-only access via "other" bits. Group membership is assigned dynamically at spawn time via `SysProcAttr.Credential.Groups` — no UIDs are statically added to `hive-coordinators` in `/etc/group`.

**Testing**: `WorkerFactory` abstraction allows injecting fake workers in unit tests. `make test` runs tests in Docker; `make test-local` runs locally with mock workers. `make test-isolation` runs isolation-specific tests requiring root and the user pool.

### Agent Lifecycle

```
agents/<name>/agent.md  →  config.LoadAgentDir()  →  Manager creates inference Loop + spawns worker
                                                                       ↓
                                                              sessions/<uuid>/
                                                                memory.md
                                                                identity.md
                                                                todos.yaml
                                                                scratch/
                                                                tmp/
                                                              db/hive.db (shared, all sessions)
```

- **Agent definitions** live in `agents/<name>/` with `agent.md` (required), optional `soul.md`, `tools.md`, and a `skills/` subdirectory.
- **Sessions** are runtime state stored in `sessions/<uuid>/`. Persistent and coordinator agents survive restarts via `RestoreSessions()`.
- **Agent mode** (ephemeral, persistent, coordinator) is a **runtime property**, not part of the agent definition. The same agent definition can be launched in different modes. Mode is specified by the caller at session creation time (`CreateSession` takes a `mode` parameter). The `spawn_session` tool accepts a `mode` parameter (defaulting to ephemeral).
- **Ephemeral sessions** run a single prompt and are cleaned up automatically.
- **Persistent sessions** get extra tools: `memory_read/write`, `todos`, `history_search/recall`.
- **Coordinator sessions** are a superset of persistent — they additionally get agent management tools (`resume_session`, `stop_session`, `send_message`, `list_sessions`) and write access to `agents/` and `skills/` directories via the `hive-coordinators` Unix group.

### Agent Definition Structure

```
agents/<name>/
  agent.md          # Required. YAML frontmatter (name, model, description, tools) + markdown body (system prompt)
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

A platform-level `skills/` directory provides shared skills available to all agents. Agent-specific skills take precedence over shared skills with the same name.

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
5. `## Available Secrets` + secret names (if any)
6. `agent.md` body (main instructions)
7. `## Tool Notes` + `tools.md` (if present)
8. `## Skills` + skill name/description listing (if present)

Skills are re-scanned from disk each turn (like memory and identity), so runtime-created skills take effect immediately. The full skill body is NOT in the prompt — agents read it on demand via `read_file`.

### Key Packages

- **`cmd/hive`** — Entry point. `run()` starts the control plane (HTTP server, manager, database). `runAgent()` is the worker entry point (reads SpawnConfig from stdin, registers tools, serves ExecuteTool gRPC).
- **`internal/agent`** — Session management. `Manager` supervises session lifecycles, spawns workers via `WorkerFactory`, creates inference loops. `CreateLanguageModel` handles LLM provider setup.
- **`internal/inference`** — Inference orchestration (runs in the control plane). `Loop` drives `fantasy.Agent.Stream()` per session. Includes system prompt assembly, context assembly from the platform DB, LLM-driven compaction, tool proxy (dispatches remote tools to workers via gRPC), and all local tools (memory, todos, history search, spawn, coordinator, skills). `ScopedManager` enforces descendant scoping. Context-based cycle detection prevents re-entrant deadlocks.
- **`internal/platform/db`** — Unified SQLite database (`db/hive.db`). Stores sessions, messages, summaries, context items, usage events, and request logs. Single writer (control plane), WAL mode, FTS5 for full-text search.
- **`internal/ipc`** — IPC interfaces and types. `AgentWorker` (control plane→worker: `ExecuteTool` + `Shutdown`), `HostManager` (inference loop→manager), `SpawnConfig` (passed to workers at startup).
- **`internal/ipc/grpcipc`** — gRPC adapters: `WorkerServer`/`WorkerClient` for AgentWorker.
- **`internal/uidpool`** — Pre-allocated Unix UID pool for per-agent user isolation. Pure bookkeeping (no OS calls). Manager acquires/releases UIDs on agent start/stop.
- **`internal/agent/tools/`** — Built-in tool implementations (read_file, write_file, edit, multiedit, bash, job_output, job_kill, glob, grep, list_files, fetch). These run in worker processes.
- **`internal/controlplane`** — Operator-level config (secrets, tool policies). Read from `config.yaml` at startup, held in memory, written on shutdown. Slash command handler for `/secrets` and `/tools` commands.
- **`internal/config`** — Markdown+YAML parsing, agent/skill config loading, memory/todos persistence.
- **`internal/hub`** — Swarm management: tracks connected workers and dispatches tasks by skill.
- **`internal/transport`** — Wire protocol (WebSocket JSON envelopes) for leader↔worker communication.
- **`internal/api`** — HTTP server with REST endpoints (`/api/health`, `/api/agents`, `/api/agents/{id}/messages`) and WebSocket chat (`/ws/chat`).
- **`web/`** — Embedded React UI (Vite + TypeScript + React 19). Built assets in `web/ui/dist/` are embedded via `//go:embed`. Strict TypeScript (`noUnusedLocals`, `noUnusedParameters`).

## Agent Tools

### Built-in Tools (11 total, agents must declare which they use)

Implementations in `internal/agent/tools/*.go`. These run in worker processes and are dispatched via `ExecuteTool` gRPC from the control plane.

| Tool | Purpose | Key Params | Constraints |
|------|---------|------------|-------------|
| `read_file` | Read file contents with line numbers | `path`, `offset`, `limit` | 64KB max output |
| `write_file` | Write full content to file (creates dirs) | `path`, `content` | Full replacement only |
| `edit_file` | Surgical find-and-replace edits | `file_path`, `old_string`, `new_string`, `replace_all` | Single match must be unique; empty `old_string` + content = create file |
| `multiedit_file` | Batch multiple edits to one file | `file_path`, `edits` (array of `{old_string, new_string, replace_all}`) | Edits applied sequentially; partial success supported |
| `list_files` | List directory contents | `path`, `pattern` (glob) | Max 500 entries; skips node_modules, vendor, dist, .git, hidden dirs |
| `glob` | Find files by glob pattern | `pattern`, `path` | Max 100 results; uses ripgrep if available, falls back to Go; sorted by mod time (newest first) |
| `grep` | Search file contents with regex | `pattern`, `path`, `include` (file glob), `literal_text` | Max 100 matches; 30s timeout; uses ripgrep if available |
| `bash` | Execute shell commands | `command`, `working_dir`, `run_in_background` | 120s timeout (sync), 32KB max output; auto-backgrounds after 60s |
| `job_output` | Get output from background job | `job_id`, `wait` | Returns stdout/stderr and completion status |
| `job_kill` | Terminate a background job | `job_id` | Immediately terminates the process |
| `fetch` | Fetch URL content | `url` | 30s timeout, 64KB max response; runs in parallel |

### Spawn Tool (all agents)

All agents get `spawn_session`. The `mode` parameter controls behavior — non-coordinator agents are restricted to ephemeral. Defined in `internal/inference/local_tools.go`.

| Tool | Purpose | Key Params | Behavior |
|------|---------|------------|----------|
| `spawn_session` | Spawn a new session from an agent definition | `agent` (name), `prompt`, `mode` (ephemeral/persistent/coordinator) | Ephemeral (default): blocks until done, returns result, cleans up. Persistent/coordinator: creates long-lived session, returns ID. 32KB max result |

### Coordinator Tools (coordinator mode only)

Defined in `internal/inference/local_tools.go`. Only injected for coordinator-mode sessions. Scoped to descendants via `ScopedManager.checkDescendant()`.

| Tool | Purpose | Key Params | Behavior |
|------|---------|------------|----------|
| `resume_session` | Restart a stopped session | `session_id` | Resumes with previous memory, history, todos |
| `send_message` | Send message to child and get response | `session_id`, `message` | Blocks; scoped to descendants; serialized per-session (mutex); 32KB max result |
| `stop_session` | Stop session and its subtree | `session_id` | Stops leaf-first; cleans up ephemeral dirs; persists persistent sessions |
| `delete_session` | Permanently delete session and subtree | `session_id` | Removes all data; cannot be undone |
| `list_sessions` | List direct child sessions | *(none)* | Shows name, ID, mode, description for direct children only |

### Persistent Agent Tools (mode: persistent or coordinator)

Defined in `internal/inference/local_tools.go`. Run in the control plane process (not in workers).

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `memory_read` | Read `memory.md` from session dir | *(none)* | Returns empty if no memories yet |
| `memory_write` | Overwrite `memory.md` | `content` | Full replacement — read first to avoid data loss; 0600 perms; visible in system prompt next turn |
| `todos` | Manage task list | `todos` (array of `{content, status, active_form}`) | Full replacement; statuses: pending, in_progress, completed |
| `history_search` | Full-text search conversation history | `query`, `scope` (messages\|summaries\|all) | Max 20 results via SQLite FTS; only if history engine initialized |
| `history_recall` | Expand a summary's details | `summary_id` | Shows full text + children; depth, compression ratio, time range |

### Skill Tool (agents with skills)

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `use_skill` | Activate a skill and get full instructions | `name` | Returns full skill body + directory listing of bundled resources. Only present when agent has skills available. |

### Tool Totals by Agent Type

- **Ephemeral sessions:** 11 built-in + 1 spawn = 12 tools (+ 1 if skills)
- **Persistent sessions:** 11 built-in + 1 spawn + 2 memory + 1 todos + 2 history = 17 tools (+ 1 if skills)
- **Coordinator sessions:** 11 built-in + 1 spawn + 5 coordinator + 2 memory + 1 todos + 2 history = 22 tools (+ 1 if skills)

## Coordinator Agent

The coordinator (`agents/coordinator/agent.md`) is the top-level agent, started in coordinator mode at bootstrap.

**Bootstrap flow** (`cmd/hive/main.go`):
1. Check `HIVE_API_KEY` is set
2. Create `Manager` with provider/API key
3. `RestoreSessions()` — resume any persistent agents from prior runs
4. `SessionByAgentName("coordinator")` — check if already running (from restore)
5. If not running, `CreateSession(ctx, "coordinator", "", "coordinator")` — no parent, coordinator mode, becomes root

Coordinator mode gives persistent-agent capabilities (memory, todos, history) plus coordinator-only tools (`resume_session`, `stop_session`, `send_message`, `list_sessions`, `delete_session`) and write access to `agents/` and `skills/` via the `hive-coordinators` Unix group. All agents get `spawn_session` which supports all modes (non-coordinators are restricted to ephemeral).

## Control Plane

The control plane (`internal/controlplane`) manages operator-level configuration that agents cannot access or modify.

**Config file:** `config.yaml` at platform root. Read at startup into Go memory. Written back on shutdown. During runtime, Go memory is authoritative.

```yaml
secrets:
  GITHUB_TOKEN: ghp_xxxxxxxxxxxx

agents:
  researcher:
    tools: [read_file, glob, grep]  # restrict below declared tools
```

**Key concepts:**

- **Secrets** — Named key-value pairs. Injected as env vars into bash commands. Agents see names in system prompt but never values.
- **Tool allowlists** — Agents declare tools in `agent.md` frontmatter (`tools: [bash, read_file, ...]`). Closed by default: no declaration = no built-in tools. Control plane can further restrict.
- **Inherited caps** — Child effective tools = intersection of (declared tools ∩ control plane ∩ parent's effective tools).
- **Bash is binary** — Agent gets bash or doesn't. No sandboxing pretense.

**Slash commands** (intercepted in WebSocket handler, never reach agent):

| Command | Effect |
|---------|--------|
| `/secrets set NAME=VALUE` | Store a secret |
| `/secrets rm NAME` | Remove a secret |
| `/secrets list` | List secret names (not values) |
| `/tools set <agent> <tools>` | Set tool override |
| `/tools rm <agent>` | Clear override |
| `/tools list [agent]` | Show overrides |

## Creating Agents at Runtime

Agents can create new agent definitions at runtime using their file tools:
1. Use `write_file` / `edit_file` to create `agents/<name>/agent.md` (and optionally `soul.md`, `tools.md`, `skills/*.md`)
2. Use `spawn_session` with mode `persistent` or `coordinator` to start the new agent — `LoadAgentDir()` is called fresh each time, so it picks up the new definition immediately
3. No restart or reload mechanism needed

Similarly, skills can be added by writing `.md` files to an agent's `skills/` directory (flat or directory format). Skills are re-scanned from disk each turn, so new skills take effect on the next inference loop turn.

## Conversation Modes

- **Coordinator and persistent agents** use the unified platform database (`db/hive.db`) — messages are stored in SQLite, automatically compacted via LLM summarization (async, per-session locking), and assembled within a token budget. The `internal/inference` package handles assembly and compaction.
- **Ephemeral agents** keep messages in-memory only (discarded on stop).
- **WebSocket chat** sends messages to the coordinator's inference loop. Streaming events flow directly from the control plane to the WebSocket (no gRPC relay).

### Agent Tool Scoping

Coordinator tools (`resume_session`, `stop_session`, `send_message`, `list_sessions`, `delete_session`) and `spawn_session` are scoped to the calling agent's descendants via `IsDescendant()`. An agent cannot manage siblings or ancestors.

## Testing Notes

- Manager tests inject a `testWorkerFactory` that returns fake `ipc.AgentWorker` implementations — no real processes or LLM calls. Without a provider configured, sessions have no inference loop (SendMessage returns an error).
- Platform DB tests (`internal/platform/db`) test the unified database schema, CRUD operations, FTS search, usage tracking, and cascade deletes.
- The `tools/` package tests run actual file/process operations in temp directories.
- gRPC adapter tests use `bufconn` (in-memory gRPC) for fast, socket-free testing of `ExecuteTool` and `Shutdown` RPCs.
- CGO is not required — SQLite uses `modernc.org/sqlite` (pure Go). `CGO_ENABLED=0` in Docker build.
- Files tagged `//go:build online` contain integration tests that hit real APIs — excluded from normal test runs.
- `make test` runs tests in Docker (`Dockerfile.testing`). `make test-local` runs locally with mock workers.
- In Docker, each worker runs as a separate Unix user (from a pre-created pool). Session dirs are private (`0700`), shared files are collaborative (setgid `2775`), and `config.yaml` is root-only (`0600`). Coordinator agents get `hive-coordinators` as a supplementary group for `agents/`/`skills/` write access. Outside Docker, isolation is disabled (no `hive-agents` group).
