# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Hiro?

Hiro is a distributed AI agent platform written in Go. A single binary serves an HTTP API, a WebSocket chat endpoint, and an embedded React dashboard. Agents are defined as markdown files with YAML frontmatter; they run agentic loops backed by [charm.land/fantasy](https://charm.land/fantasy) and can spawn/manage child agents.

## CRITICAL: Docker Only

**NEVER run `./hiro` directly.** This app only runs in Docker. To test changes, rebuild and start Docker:

```bash
docker compose -f docker-compose.dev.yml up --build -d
```

Running locally creates runtime directories (`agents/`, `db/`, `instances/`, etc.) in the repo root and conflicts with Docker port bindings.

## Build & Dev Commands

```bash
make build               # Build web UI + Go binary (outputs ./hiro)
make build-dev           # Build Go binary without web UI (uses -tags dev)
make web                 # Build web UI only (cd web/ui && npm install && npm run build)
make test                # Run tests in Docker (builds test container)
make test-local          # Run tests locally (no Docker, uses mock workers)
make test-isolation      # Run UID isolation tests in Docker (requires user pool)
make test-online         # E2E tests against real LLM in Docker (requires HIRO_API_KEY)
make test-cluster        # Cluster e2e tests: leader + worker topology (requires HIRO_API_KEY)
make test-cluster-relay  # Cluster e2e tests via relay server (requires HIRO_API_KEY)
make check               # test + go vet (in Docker)
make lint                # Run golangci-lint (must pass before committing)
make docker              # Docker build (docker compose build)
make docker-up           # Start Docker containers (docker compose up)
make docker-down         # Stop Docker containers (docker compose down)
make proto               # Regenerate protobuf (requires protoc)
make clean               # Remove binary and web/ui/dist
```

Run a single test:
```bash
go test ./internal/platform/db/... -run TestCompaction -v -count=1
```

Run integration tests that hit real APIs (excluded from normal `make test`):
```bash
go test ./internal/agent/... -tags=online -v -count=1
```

### E2E Tests

E2E tests (`tests/e2e/`) run against a real Hiro server in Docker with a real LLM provider. They require `HIRO_API_KEY` (sourced from `.env`).

```bash
# Source .env and run e2e tests — builds Docker image, starts server, runs tests, tears down
set -a; . .env; set +a; make test-online

# Cluster e2e tests (leader + worker topology)
set -a; . .env; set +a; make test-cluster
```

Key details:
- Tests talk to the operator via WebSocket, instructing it to use specific tools by name
- If you rename or split tools, update the prompts in `tests/e2e/e2e_test.go` (e.g. `spawnPersistentAgent` tells the operator which tool to call)
- The `SessionClear` test is known to be flaky due to OpenRouter connection timeouts
- Tests take ~2-6 minutes depending on LLM latency

Web UI dev server (separate terminal — proxies `/api` and `/ws` to `localhost:8080`):
```bash
cd web/ui && npm run dev
```

## Configuration

LLM provider and API key are configured through the web UI onboarding flow on first launch, stored in `config/config.yaml`. Provider settings can be updated later via the dashboard settings page.

### Environment Variables

These are optional overrides, not required for normal operation:

| Variable | Default | Purpose |
|---|---|---|
| `HIRO_ADDR` | `:8080` | HTTP listen address |
| `HIRO_ROOT` | `.` | Platform root containing `agents/`, `instances/`, `skills/`, `workspace/` |
| `HIRO_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |
| `HIRO_LOG_LEVEL` | `info` | Log level |

E2E tests use `HIRO_API_KEY` in `.env` to provide credentials to the test container — this is a test-only concern, not part of normal operation.

A `.env` file is loaded automatically via godotenv (does not override existing vars).

## Architecture

### Process Model

Hiro uses **process isolation**: the control plane owns all inference (LLM calls, conversation history, system prompt assembly) while tool execution runs in separate worker processes under isolated Unix UIDs. Communication is via gRPC over Unix sockets.

```
Control Plane Process (hiro)
├── HTTP/WS API (web UI, REST, chat)
├── Inference loops (fantasy agent per instance)
├── Unified database (db/hiro.db — instances, sessions, messages, usage, logs)
├── System prompt assembly, context providers, compaction
├── Local tools: memory, todos, history, spawn, management, skills
├── Secrets + tool policies + tool rules (config/config.yaml)
├── Process registry + instance lifecycle
└── Spawns: hiro agent (one worker per session)

Agent Worker Process (hiro agent)
├── Tool execution sandbox (Bash, file ops, Glob, Grep, WebFetch)
├── gRPC AgentWorker server (ExecuteTool + Shutdown only)
└── Runs under isolated UID for security
```

**Spawn protocol**: Control plane spawns `hiro agent`, pipes `SpawnConfig` as JSON to stdin. Worker starts a gRPC server on a Unix socket (`/tmp/hiro-agent-{session-id}.sock`) and writes "ready" to stdout. Control plane connects and dispatches tool calls via `ExecuteTool` RPC. When UID isolation is enabled, each worker runs as a dedicated Unix user via `SysProcAttr.Credential`.

**Unix user isolation**: Auto-detected at startup (enabled iff `hiro-agents` group exists). A pre-created pool of 64 Unix users (`hiro-agent-0` through `hiro-agent-63`, UIDs 10000-10063) provides per-agent isolation. Instance dirs are `chown`ed to the agent's UID. Workspace uses setgid (`2775`) for collaborative file access. The control plane runs as root inside Docker for UID switching. The `config/` directory is `0700` root-owned, unreadable by agents.

**Group-based access control**: Two Unix groups control filesystem access:
- `hiro-agents` (GID 10000) — primary group for all agent UIDs. Grants read/write to `/hiro` and `/opt/mise`.
- `hiro-operators` (GID 10001) — supplementary group for agents that need write access to `agents/` and `skills/` directories (setgid `2775`). Other agents get read-only access via "other" bits. Group membership is declared in agent frontmatter (`groups: [hiro-operators]`) and assigned dynamically at spawn time via `SysProcAttr.Credential.Groups`.

**Testing**: `WorkerFactory` abstraction allows injecting fake workers in unit tests. `make test` runs tests in Docker; `make test-local` runs locally with mock workers. `make test-isolation` runs isolation-specific tests requiring root and the user pool.

### Agent Lifecycle

```
agents/<name>/agent.md  →  config.LoadAgentDir()  →  Manager creates inference Loop + spawns worker
                                                                       ↓
                                                              instances/<uuid>/
                                                                config.yaml (root-owned, 0600)
                                                                persona.md
                                                                memory.md
                                                                sessions/<session-id>/
                                                                  todos.yaml
                                                                  scratch/
                                                                  tmp/
                                                              db/hiro.db (shared, all instances)
```

- **Agent definitions** live in `agents/<name>/` with `agent.md` (required) and an optional `skills/` subdirectory.
- **Instances** are durable agent identities stored in `instances/<uuid>/`. Instance-level state includes `config.yaml` (control-plane-managed, root-owned), `persona.md`, and `memory.md`. Persistent instances survive restarts via `RestoreInstances()`.
- **Sessions** are task-scoped work within an instance. An instance has a single active session at a time. Session-level state includes `todos.yaml` (created on first TodoWrite call), `scratch/`, and `tmp/`. A new session is created on `/clear`, replacing the previous one.
- **Agent mode** (ephemeral, persistent) is a **runtime property**, not part of the agent definition. The same agent definition can be launched in different modes. Mode is specified by the caller at instance creation time (`CreateInstance` takes a `mode` parameter). The `SpawnInstance` tool accepts a `mode` parameter (defaulting to ephemeral).
- **Ephemeral instances** run a single prompt and are cleaned up automatically.
- **Persistent instances** get extra tools: `AddMemory`/`ForgetMemory`, `TodoWrite`, `HistorySearch`/`HistoryRecall`. Persona and memory are managed via the standard file tools (`persona.md` and `memory.md` in the instance directory). Management tools (`CreatePersistentInstance`, `ResumeInstance`, `StopInstance`, `SendMessage`, `ListInstances`) are available to any agent that declares them in `allowed_tools`, scoped to descendants.

### Agent Definition Structure

```
agents/<name>/
  agent.md          # Required. YAML frontmatter (name, description, tools) + markdown body (system prompt)
  skills/
    flat-skill.md           # Flat file skill
    dir-skill/
      SKILL.md              # Directory skill (can bundle scripts/, references/, assets/)
      scripts/
      references/
      assets/
```

A platform-level `skills/` directory provides shared skills available to all agents. Agent-specific skills take precedence over shared skills with the same name.

Skills use **progressive disclosure** — only name and description are listed in the system prompt. The agent activates a skill via the `Skill` tool, which returns the full instructions and lists bundled resources.

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

### System Prompt and Context Messages

The system prompt contains only **static identity** — content that rarely changes:

1. `## Environment` + directory tree with instance/session paths
2. `agent.md` body (main instructions, reloaded from disk each turn)
3. `## Persona` + `persona.md` from instance dir
4. `## Security` + tool result trust warning

**Dynamic state** (memories, todos, secrets, skills, agent listings) is injected as `<system-reminder>` user messages via the **context provider system**. These messages are persisted in conversation history with delta tracking metadata — if nothing changed since the last turn, no new message is emitted and the prompt cache is preserved. Multiple providers that emit in the same turn are merged into a single message.

See [`docs/system-reminders.md`](docs/system-reminders.md) for the full design, change detection strategies, and how to add new providers.

### Key Packages

- **`cmd/hiro`** — Entry point. `run()` starts the control plane (HTTP server, manager, database). `runAgent()` is the worker entry point (reads SpawnConfig from stdin, registers tools, serves ExecuteTool gRPC). `bootstrap.go` extracts cluster setup helpers.
- **`internal/agent`** — Instance management. `Manager` supervises instance lifecycles, spawns workers via `WorkerFactory`, creates inference loops. Split into focused files: lifecycle, session, query, worker, resolve, restore, helpers.
- **`internal/inference`** — Inference orchestration (runs in the control plane). `Loop` drives `fantasy.Agent.Stream()` per instance. Includes system prompt assembly, context providers with delta tracking, LLM-driven compaction, tool proxy (dispatches remote tools to workers via gRPC), and all local tools (memory, todos, history search, spawn, management, skills). `ScopedManager` enforces descendant scoping via instance IDs.
- **`internal/platform/db`** — Unified SQLite database (`db/hiro.db`). Stores instances, sessions, messages, summaries, context items, usage events, request logs, and structured logs. Single writer (control plane), WAL mode, FTS5 for full-text search.
- **`internal/toolrules`** — Tool permission rules engine. Parses parameterized rules like `Bash(curl *)`, uses a real shell parser (`mvdan.cc/sh/v3`) for Bash command analysis, and enforces layered allow/deny at call time. See [`docs/tool-permissions.md`](docs/tool-permissions.md).
- **`internal/cluster`** — Leader/worker clustering over gRPC with mTLS. Includes file sync, relay for NAT traversal, tracker-based discovery, node approval, and remote terminal sessions.
- **`internal/ipc`** — IPC interfaces and types. `AgentWorker` (control plane→worker: `ExecuteTool` + `Shutdown`), `HostManager` (inference loop→manager), `SpawnConfig` (passed to workers at startup).
- **`internal/ipc/grpcipc`** — gRPC adapters: `WorkerServer`/`WorkerClient` for AgentWorker.
- **`internal/uidpool`** — Pre-allocated Unix UID pool for per-agent user isolation. Pure bookkeeping (no OS calls). Manager acquires/releases UIDs on agent start/stop.
- **`internal/agent/tools/`** — Built-in tool implementations (Read, Write, Edit, Bash, TaskOutput, TaskStop, Glob, Grep, WebFetch). These run in worker processes. Resource limits centralized in `limits.go`.
- **`internal/controlplane`** — Operator-level config (secrets, cluster settings). Read from `config/config.yaml` at startup, held in memory, written on shutdown. Slash command handler for `/secrets` and `/cluster` commands.
- **`internal/config`** — Markdown+YAML parsing, agent/skill config loading, persona/memory/todos persistence.
- **`internal/api`** — HTTP server with REST endpoints, WebSocket chat (`/ws/chat`), WebSocket terminal (`/ws/terminal`), file browser, sharing, usage tracking, log querying/streaming, and cluster node management.
- **`internal/transport`** — Wire protocol (WebSocket JSON envelopes) for leader↔worker communication.
- **`internal/hub`** — Swarm management: tracks connected workers and dispatches tasks by skill.
- **`internal/platform/loghandler`** — Structured slog handler for platform-wide log capture and querying.
- **`internal/provider`** — LLM provider construction (`CreateLanguageModel`, `TestConnection`, `AvailableProviders`). Isolates SDK imports.
- **`web/`** — Embedded React UI (Vite + TypeScript + React 19). Built assets in `web/ui/dist/` are embedded via `//go:embed`. Strict TypeScript (`noUnusedLocals`, `noUnusedParameters`). Pages organized by feature: chat, files, settings, terminal, logs, shared.

## Agent Tools

### Built-in Tools (9 total, agents must declare which they use)

Implementations in `internal/agent/tools/*.go`. These run in worker processes and are dispatched via `ExecuteTool` gRPC from the control plane.

| Tool | Purpose | Key Params | Constraints |
|------|---------|------------|-------------|
| `Read` | Read file contents with line numbers | `file_path`, `offset`, `limit` | 64KB max output |
| `Write` | Write full content to file (creates dirs) | `file_path`, `content` | Full replacement only |
| `Edit` | Surgical find-and-replace edits | `file_path`, `old_string`, `new_string`, `replace_all` | Single match must be unique; empty `old_string` + content = create file |
| `Glob` | Find files by glob pattern | `pattern`, `path` | Max 100 results; uses ripgrep if available, falls back to Go; sorted by mod time (newest first) |
| `Grep` | Search file contents with regex | `pattern`, `path`, `glob`, `type`, `output_mode`, `A`/`B`/`C`/`context`, `n`, `i`, `head_limit`, `offset`, `multiline`, `literal_text` | 3 output modes (content/files_with_matches/count); 30s timeout; default 250 results |
| `Bash` | Execute shell commands | `command`, `working_dir`, `timeout`, `description`, `run_in_background` | Default auto-background after 60s; `timeout` overrides (max 600000ms); 32KB max output |
| `TaskOutput` | Get output from background task | `task_id`, `block` | Returns stdout/stderr and completion status; block (default true) waits for completion |
| `TaskStop` | Stop a running background task | `task_id` | Immediately terminates the process |
| `WebFetch` | Fetch URL content | `url` | 30s timeout, 64KB max response; runs in parallel |

### Spawn Tool (all agents)

All agents get `SpawnInstance`. Non-persistent agents are restricted to ephemeral mode. Defined in `internal/inference/tools_spawn.go`.

| Tool | Purpose | Key Params | Behavior |
|------|---------|------------|----------|
| `SpawnInstance` | Run an agent to complete a task | `agent` (name), `prompt`, `background` (bool) | Blocks until done, returns result, cleans up. `background: true`: returns immediately, notifies on completion. 32KB max result |

### Management Tools

Defined in `internal/inference/tools_spawn.go`. Available to any agent that declares them in `allowed_tools`. Scoped to descendants via `ScopedManager.checkDescendant()`.

| Tool | Purpose | Key Params | Behavior |
|------|---------|------------|----------|
| `CreatePersistentInstance` | Create a long-lived agent instance | `agent` (name) | Returns instance ID. Interact via SendMessage. |
| `ResumeInstance` | Restart a stopped instance | `instance_id` | Resumes with previous memory, history, todos |
| `SendMessage` | Send message to child and get response | `instance_id`, `message` | Blocks; scoped to descendants; serialized per-instance (mutex); 32KB max result |
| `StopInstance` | Stop instance and its subtree | `instance_id` | Stops leaf-first; cleans up ephemeral dirs; persists persistent instances |
| `DeleteInstance` | Permanently delete instance and subtree | `instance_id` | Removes all data; cannot be undone |
| `ListInstances` | List direct child instances | *(none)* | Shows name, ID, mode, description for direct children only |

### Persistent Agent Tools (mode: persistent)

Defined in `internal/inference/tools_todos.go`, `tools_memory.go`, `tools_history.go`. Run in the control plane process (not in workers). Persona is managed via the standard file tools (`persona.md` is seeded at instance creation and included in the system prompt).

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `AddMemory` | Append a memory entry | `content` (single line) | Appends to `memory.md` with date stamp; evicts oldest when over 100 entries |
| `ForgetMemory` | Remove memory entries | `match` (substring) | Case-insensitive match against content (not date stamps); removes all matches |
| `TodoWrite` | Manage task list | `todos` (array of `{content, status, active_form}`) | Full replacement; statuses: pending, in_progress, completed |
| `HistorySearch` | Full-text search conversation history | `query`, `scope` (messages\|summaries\|all) | Max 20 results via SQLite FTS; searches current session only |
| `HistoryRecall` | Expand a summary's details | `summary_id` | Shows full text + children; depth, compression ratio, time range |

### Schedule Tools (mode: persistent)

Defined in `internal/inference/tools_schedule.go`, `tools_notify.go`. Run in the control plane. Agents must declare these in `allowed_tools`. See [`docs/scheduling.md`](docs/scheduling.md).

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `ScheduleRecurring` | Create a cron-based recurring schedule | `name`, `schedule` (cron expr), `message` | Fires in isolated triggered session; server timezone |
| `ScheduleOnce` | Create a one-time schedule | `name`, `at` (duration or datetime), `message` | Auto-deleted after successful fire |
| `CancelSchedule` | Remove a schedule by name | `name` | Removes from DB and scheduler heap |
| `ListSchedules` | List all schedules for this instance | *(none)* | Shows name, type, schedule, status, next fire, fire/error counts |
| `Notify` | Push message to user's primary session | `message` | Only available in triggered sessions; instance-scoped delivery |

### Skill Tool (agents with skills)

| Tool | Purpose | Key Params | Notes |
|------|---------|------------|-------|
| `Skill` | Activate a skill and get full instructions | `name` | Returns full skill body + directory listing of bundled resources. Only present when agent has skills available. |

### Tool Totals by Agent Type

- **Ephemeral instances:** 9 built-in + 1 spawn = 10 tools (+ 1 if skills)
- **Persistent instances:** 9 built-in + 1 spawn + 2 memory + 1 todos + 2 history + 4 schedule = 19 tools (+ 1 if skills, + management tools if declared in allowed_tools)

## Operator Agent

The operator (`agents/operator/agent.md`) is the top-level agent, started as a persistent instance at bootstrap.

**Bootstrap flow** (`cmd/hiro/main.go` + `bootstrap.go`):
1. Load `config/config.yaml` — if no provider is configured, the server starts in setup mode (dashboard shows onboarding)
2. Once configured, create `Manager` with provider from config
3. `RestoreInstances()` — resume any persistent agents from prior runs
4. `InstanceByAgentName("operator")` — check if already running (from restore)
5. If not running, `CreateInstance(ctx, "operator", "", "persistent")` — no parent, persistent mode, becomes root

The operator agent declares management tools (`CreatePersistentInstance`, `ResumeInstance`, `StopInstance`, `DeleteInstance`, `SendMessage`, `ListInstances`, `ListNodes`) and schedule tools (`ScheduleRecurring`, `ScheduleOnce`, `CancelSchedule`, `ListSchedules`) in its `allowed_tools` frontmatter and `groups: [hiro-operators]` for write access to `agents/` and `skills/`. All agents get `SpawnInstance`.

## Control Plane

The control plane (`internal/controlplane`) manages operator-level configuration that agents cannot access or modify.

**Config file:** `config/config.yaml`. Read at startup into Go memory. Written back on shutdown. During runtime, Go memory is authoritative.

```yaml
secrets:
  GITHUB_TOKEN: ghp_xxxxxxxxxxxx
```

**Key concepts:**

- **Secrets** — Named key-value pairs. Injected as env vars into Bash commands. Agents see names in system prompt but never values.
- **Tool declarations** — Each instance owns its tool declarations in `config.yaml` (seeded from `agent.md` at creation). Closed by default: no declaration = no built-in tools. See [`docs/tool-permissions.md`](docs/tool-permissions.md).
- **Tool rules** — Parameterized rules like `Bash(curl *)` or `Read(/src/*)` provide fine-grained call-time enforcement. Deny rules like `Bash(rm *)` block specific patterns.
- **Inherited caps** — Child effective tools = intersection of (declared tools ∩ parent's effective tools).

**Slash commands** (intercepted in WebSocket handler, never reach agent):

| Command | Effect |
|---------|--------|
| `/secrets set NAME=VALUE` | Store a secret |
| `/secrets rm NAME` | Remove a secret |
| `/secrets list` | List secret names (not values) |

## Instance Config

Per-instance operational configuration lives in `instances/<uuid>/config.yaml`. This file is **root-owned with `0600` permissions** — agent worker processes cannot read or modify it. Only the control plane reads and writes this file.

```yaml
model: anthropic/claude-sonnet-4           # optional model override
reasoning_effort: high                              # optional
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch]
disallowed_tools: [Bash(rm *)]
channels:
  telegram:
    bot_token: ${TELEGRAM_BOT}                      # secret reference
    allowed_chats: [12345]
  slack:
    bot_token: ${SLACK_BOT}
    signing_secret: ${SLACK_SIGN}
    allowed_channels: ["C123"]
```

**Tool declarations** are seeded from `agent.md` at instance creation and owned by the instance thereafter. Changes to `agent.md` `allowed_tools` do not flow to existing instances — each instance's `config.yaml` is the source of truth.

**What lives where:**

| Config | Location | Why |
|--------|----------|-----|
| Model override, reasoning effort | `instances/<uuid>/config.yaml` | Per-instance operational config |
| Tool declarations | `instances/<uuid>/config.yaml` | Seeded from agent.md, instance-owned thereafter |
| Channel bindings (Telegram/Slack) | `instances/<uuid>/config.yaml` | Per-instance, multiple agents can have channels |
| Secrets | `config/config.yaml` | Operator-level, referenced by `${NAME}` |
| Persona, memory | `instances/<uuid>/persona.md`, `memory.md` | Agent-editable identity (chowned to agent UID) |

**Filesystem protection:** During `acquireUIDAndChown`, `config.yaml` is skipped — it stays root-owned while the rest of the instance directory is transferred to the agent UID. This uses the same protection model as `config/config.yaml`.

**Channel lifecycle:** Per-instance channels are managed via `InstanceLifecycleHook` (`cmd/hiro/channels.go`). When an instance starts, its `config.yaml` is read and any configured channels are created. When it stops, channels are destroyed. Each instance gets unique channel names (`telegram:<instanceID>`, `slack:<instanceID>`) and Slack webhook routes (`POST /api/instances/{id}/slack/events`).

**Implementation:** `internal/config/instance_config.go` provides `LoadInstanceConfig`/`SaveInstanceConfig`. The web channel's config message handler calls `Manager.UpdateInstanceConfig` which does a read-modify-write to preserve channel config while updating model/reasoning fields.

## Creating Agents at Runtime

Agents can create new agent definitions at runtime using their file tools:
1. Use `Write` / `Edit` to create `agents/<name>/agent.md` (and optionally `skills/*.md`)
2. Use `SpawnInstance` with mode `persistent` to start the new agent — `LoadAgentDir()` is called fresh each time, so it picks up the new definition immediately
3. No restart or reload mechanism needed

Similarly, skills can be added by writing `.md` files to an agent's `skills/` directory (flat or directory format). Skills are re-scanned from disk each turn, so new skills take effect on the next inference loop turn.

## Conversation Modes

- **Persistent agents** use the unified platform database (`db/hiro.db`) — messages are stored in SQLite, automatically compacted via LLM summarization (async, per-instance locking), and assembled within a token budget. The `internal/inference` package handles assembly and compaction.
- **Ephemeral agents** keep messages in-memory only (discarded on stop).
- **WebSocket chat** sends messages to the root instance's inference loop. Streaming events flow directly from the control plane to the WebSocket (no gRPC relay).

### Agent Tool Scoping

Management tools (`ResumeInstance`, `StopInstance`, `SendMessage`, `ListInstances`, `DeleteInstance`) and `SpawnInstance` are scoped to the calling agent's descendants via `IsDescendant()`. An agent cannot manage siblings or ancestors.

## Design Docs

The `docs/` directory contains design documents for key subsystems. **Keep these in sync when modifying the systems they describe.**

### [`docs/agent-model.md`](docs/agent-model.md) — Agent Model

The conceptual model for how agents work. Describes the three-tier hierarchy (Definition → Instance → Session), what state lives where (filesystem vs database), instance modes (ephemeral and persistent), session lifecycle, and parent-child relationships. Read this first to understand Hiro's core abstractions.

### [`docs/security.md`](docs/security.md) — Security Model

Defense-in-depth security architecture. Covers Docker containment, process isolation via separate worker processes, Unix user isolation (UID pool with 64 pre-created users), filesystem permissions (who can read/write what), the tool capability system, secrets management (names in prompts, values only in env vars), agent authorization scoping (descendants only), and IPC security (Unix sockets, no TCP). Includes a threat model of what agents can and cannot do.

### [`docs/tool-permissions.md`](docs/tool-permissions.md) — Tool Permissions

The layered tool permission system. Covers the rule format (`Tool(pattern)` with wildcards), how permissions combine across sources (instance config, parent agent, skill activation), call-time enforcement with the `toolrules` package, Bash command analysis using a real shell parser (catches `$(rm -rf /)` inside subshells), path normalization for file tools, and the complete frontmatter reference for agent and skill YAML fields.

### [`docs/system-reminders.md`](docs/system-reminders.md) — Context Provider System

How dynamic context (memories, todos, secrets, skills, agent listings) is injected into conversations without busting the prompt cache. Describes the two change detection strategies (named-set delta for sets of items, content hash for text blobs), the `DeltaReplay` metadata format, self-healing after compaction, and how to add new providers. All 5 providers are documented with their gates and data sources.

### [`docs/scheduling.md`](docs/scheduling.md) — Scheduling

How agents schedule recurring and one-time tasks. Covers the subscription data model (SQLite with JSON trigger column), the min-heap priority queue scheduler, triggered sessions (isolated inference turns with Notify for surfacing results), cron and one-shot trigger types, lifecycle integration (pause on stop, resume on start), concurrency model (lock ordering, overlap guards, WaitGroup), and the server timezone setting. Also describes the future trigger type extensibility (webhooks, file watches).

### [`docs/network-isolation.md`](docs/network-isolation.md) — Network Isolation

Per-agent network isolation design. Each agent spawns in its own network namespace (`CLONE_NEWNET` + veth pair). A DNS forwarder resolves allowed domains and dynamically populates nftables IP sets — filtering is purely at the IP layer, protocol-agnostic (HTTPS, SSH, git all work). Covers requirements, six rejected alternatives, the DNS-driven firewall design, agent configuration (`network.egress`), policy inheritance, spawn protocol changes, and two rounds of security review findings. Not yet implemented.

### [`docs/map.md`](docs/map.md) — Codebase Map

Comprehensive map of every package, file, and capability with LOC counts. Includes the capability review checklist (44 reviewable units), hotspot analysis (files over 500 LOC), and a full quality findings log (structural fixes, correctness fixes, security fixes, concurrency fixes, testing gaps — most resolved). Use this for planning work and understanding where things live.

## Testing Notes

- Manager tests inject a `testWorkerFactory` that returns fake `ipc.AgentWorker` implementations — no real processes or LLM calls. Without a provider configured, instances have no inference loop (SendMessage returns an error).
- Platform DB tests (`internal/platform/db`) test the unified database schema, CRUD operations, FTS search, usage tracking, and cascade deletes.
- The `tools/` package tests run actual file/process operations in temp directories.
- gRPC adapter tests use `bufconn` (in-memory gRPC) for fast, socket-free testing of `ExecuteTool` and `Shutdown` RPCs.
- CGO is not required — SQLite uses `modernc.org/sqlite` (pure Go). `CGO_ENABLED=0` in Docker build.
- Files tagged `//go:build online` contain integration tests that hit real APIs — excluded from normal test runs.
- `make test` runs tests in Docker. `make test-local` runs locally with mock workers.
- In Docker, each worker runs as a separate Unix user (from a pre-created pool). Instance dirs are private (`0700`), shared files are collaborative (setgid `2775`), and `config/` is root-only (`0700`). Agents with `groups: [hiro-operators]` in frontmatter get the supplementary group for `agents/`/`skills/` write access. Outside Docker, isolation is disabled (no `hiro-agents` group).
