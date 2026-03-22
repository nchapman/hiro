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

### Key Packages

- **`cmd/hive`** — Entry point. Loads config, starts manager, boots coordinator agent, runs HTTP server.
- **`internal/agent`** — Agent runtime. `Agent` wraps a `fantasy.Agent`; `Manager` supervises lifecycles, provides manager tools (spawn/start/stop/send_message/list_agents), and serializes per-agent conversations.
- **`internal/config`** — Markdown+YAML parsing, agent/skill config loading, manifest/memory/todos persistence.
- **`internal/history`** — SQLite-backed conversation history with automatic LLM-driven compaction. `Engine` coordinates `Store` (persistence) + `Compactor` (summarization) + `Assemble` (context assembly within token budget).
- **`internal/hub`** — Swarm management: tracks connected workers and dispatches tasks by skill.
- **`internal/transport`** — Wire protocol (WebSocket JSON envelopes) for leader↔worker communication.
- **`internal/api`** — HTTP server with REST endpoints (`/api/health`, `/api/agents`, `/api/agents/{id}/messages`) and WebSocket chat (`/ws/chat`).
- **`web/`** — Embedded React UI (Vite + TypeScript). Built assets in `web/ui/dist/` are embedded via `//go:embed`.

### System Prompt Assembly

Each turn, `currentSystemPrompt()` rebuilds the prompt from disk: soul → identity → memories → todos → instructions → tool notes → skills. This means `memory_write` and identity edits take effect on the next turn.

### Conversation Modes

- **Persistent agents** use `history.Engine` — messages are stored in SQLite, automatically compacted via LLM summarization, and assembled within a token budget.
- **Ephemeral agents** keep messages in-memory only.
- **WebSocket chat** creates per-connection conversations for ephemeral agents, or uses shared persistent history for persistent agents.

### Agent Tool Scoping

Manager tools (spawn/start/stop/send_message/list_agents) are scoped to the calling agent's descendants via `IsDescendant()`. An agent cannot manage siblings or ancestors.

## Testing Notes

- Tests use `fantasy.LanguageModel` injection (`opts.LM`) to avoid real API calls.
- `history` tests use `NewEngineWithSummarizer()` with a mock summarizer.
- The `tools/` package tests run actual file/process operations in temp directories.
- CGO is not required — SQLite uses `modernc.org/sqlite` (pure Go).
