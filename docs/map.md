# Hive Project Map

> Comprehensive map of every major capability, package, and subsystem.
> Use this to plan quality improvements one piece at a time.

## At a Glance

| Metric | Value |
|--------|-------|
| Go source (non-test) | ~17.9k LOC across 92 files |
| Go test code | ~18.6k LOC across 69 test files |
| Frontend (TS/TSX) | ~6.6k LOC across 44 files |
| Internal packages | 15 |
| Top-level commands | 3 (`main.go`, `agent.go`, `worker_node.go`) |

---

## 1. Entry Points (`cmd/hive/`)

| File | LOC | Role |
|------|-----|------|
| `main.go` | 406 | CLI parsing, `run()` starts control plane (HTTP, manager, DB), `runAgent()` is worker entry |
| `agent.go` | ~150 | Agent worker subprocess — reads SpawnConfig from stdin, serves gRPC |
| `worker_node.go` | 329 | Worker node for cluster mode — connects to leader, bridges remote tool calls |

**Bootstrap flow**: parse flags → load `.env` → open DB → create Manager → restore persistent instances → start coordinator → start HTTP server.

---

## 2. Agent Management (`internal/agent/`)

The core of instance lifecycle management. Split into focused files (was 1,742 LOC monolith).

| File | LOC | Role |
|------|-----|------|
| `manager.go` | 155 | Core types (Manager, instance, InstanceInfo, WorkerHandle), constructor, registry primitives |
| `manager_lifecycle.go` | 449 | CreateInstance, SpawnEphemeral, StopInstance, DeleteInstance, startInstance, Shutdown |
| `manager_session.go` | 415 | StartInstance (restart), NewSession, UpdateInstanceConfig, config push/watch |
| `manager_query.go` | 209 | GetInstance, ListInstances, GetHistory, InstanceByAgentName, IsDescendant |
| `manager_worker.go` | 181 | shutdownHandle, cleanupWorker, softStop, watchWorker, removeInstance |
| `manager_resolve.go` | 159 | computeEffectiveTools, buildAllowedToolsMap, resolveProvider/Model |
| `manager_helpers.go` | 147 | SendMessage, SecretNames/Env, path helpers, validateAgentName |
| `manager_restore.go` | 106 | RestoreInstances (startup recovery from DB) |
| `agent.go` | ~180 | Agent struct, LoadAgentDir, agent definition types |
| `spawn.go` | ~180 | Worker process spawning (exec, UID switching, stdin pipe) |
| `tool_executor.go` | ~120 | ToolExecutor bridges inference loop → worker gRPC for remote tool calls |

### `internal/agent/tools/` — Built-in Tool Implementations

All run in **worker processes**, dispatched via gRPC.

| File | LOC | Tool |
|------|-----|------|
| `bash.go` | ~200 | Shell execution, 120s timeout, auto-background |
| `background.go` | 240 | Background job registry (start, poll, kill) |
| `grep.go` | 368 | Regex search with ripgrep fallback to Go |
| `glob.go` | 256 | File pattern matching, ripgrep or Go fallback |
| `edit.go` | ~180 | Surgical find-and-replace |
| `multiedit.go` | ~150 | Batch edits to one file |
| `read_file.go` | ~120 | Read with offset/limit, 64KB cap |
| `write_file.go` | ~100 | Atomic file write (temp+rename), auto-mkdir |
| `list_files.go` | ~120 | Directory listing with glob filter |
| `fetch.go` | ~100 | HTTP fetch, 64KB response cap |
| `job_output.go` | ~80 | Background job stdout/stderr |
| `job_kill.go` | ~50 | Terminate background job |
| `resolve.go` | ~120 | Path resolution, sandboxing, symlink confinement, atomicWriteFile |
| `rg.go` | ~60 | Ripgrep detection helper |

**Tests**: Every tool has a corresponding `*_test.go`. Tests run real file/process ops in temp dirs.

---

## 3. Inference Engine (`internal/inference/`)

Runs in the **control plane process**. Drives the agentic loop per instance.

| File | LOC | Role |
|------|-----|------|
| `loop.go` | 564 | Main inference loop — calls `fantasy.Agent.Stream()`, handles tool dispatch |
| `tools_spawn.go` | 251 | Spawn tool + coordinator tools (ScopedManager, list_nodes, send_message, etc.) |
| `tools_history.go` | 111 | history_search, history_recall |
| `tools_skills.go` | 125 | use_skill + path validation |
| `tools_todos.go` | 76 | todos tool |
| `tools_memory.go` | 43 | memory_read, memory_write |
| `compaction.go` | 682 | LLM-driven conversation summarization (now with MaxSummaryDepth cap) |
| `assembly.go` | 152 | Message assembly within token budget (now with fresh tail overflow protection) |
| `context.go` | ~180 | Context item management (system prompt sections) |
| `prompt.go` | ~200 | System prompt builder (soul + identity + memory + todos + agent.md + tools.md + skills) |
| `tools.go` | ~180 | Tool proxy — routes calls to local or remote (worker) execution |
| `helpers.go` | ~120 | Token counting, message utilities |
| `redact.go` | ~80 | Secret redaction in tool outputs |

### Local Tools (run in control plane, not workers)

| Tool | Scope | Purpose |
|------|-------|---------|
| `spawn_instance` | All agents | Spawn child instance (ephemeral/persistent/coordinator) |
| `memory_read` / `memory_write` | Persistent+ | Read/write `memory.md` |
| `todos` | Persistent+ | Manage task list (YAML) |
| `history_search` / `history_recall` | Persistent+ | FTS search conversation history |
| `resume_instance` | Coordinator | Restart stopped child |
| `send_message` | Coordinator | Message child, get response |
| `stop_instance` | Coordinator | Stop child + subtree |
| `delete_instance` | Coordinator | Permanently remove child + subtree |
| `list_instances` | Coordinator | List direct children |
| `use_skill` | Agents with skills | Load skill instructions on demand |

**Tests**: `assembly_test.go`, `compaction_test.go`, `context_test.go`, `prompt_test.go`, `tools_test.go`, `helpers_test.go`, `redact_test.go`, plus two online eval tests.

---

## 4. Database (`internal/platform/db/`)

Unified SQLite database (`db/hive.db`). Single writer, WAL mode, FTS5 for search. Pure Go SQLite via `modernc.org/sqlite`.

| File | LOC | Role |
|------|-----|------|
| `messages.go` | 613 | Message CRUD, FTS indexing, summary storage, context assembly queries |
| `db.go` | ~300 | Schema, migrations, connection setup, WAL config |
| `usage.go` | 234 | Token/cost usage tracking and aggregation |
| `instances.go` | ~200 | Instance CRUD, parent-child relationships |
| `sessions.go` | ~150 | Session lifecycle, cascade deletes |

**Schema entities**: instances, sessions, messages, summaries, context_items, usage_events, request_log.

**Tests**: `db_test.go` covers schema, CRUD, FTS, cascades, usage.

---

## 5. Config & Parsing (`internal/config/`)

| File | LOC | Role |
|------|-----|------|
| `markdown.go` | 394 | YAML frontmatter + markdown parser, agent/skill config loading |
| `memory.go` | ~140 | Memory.md read/write helpers (atomic write) |
| `todos.go` | ~100 | Todos YAML read/write helpers (atomic write) |

**Tests**: `markdown_test.go`, `memory_test.go`, `todos_test.go`.

---

## 6. IPC & gRPC (`internal/ipc/`)

Interfaces and types for control plane ↔ worker communication.

| File | LOC | Role |
|------|-----|------|
| `worker.go` | ~80 | `AgentWorker` interface (ExecuteTool + Shutdown) |
| `host_manager.go` | ~80 | `HostManager` interface (inference→manager callbacks) |
| `types.go` | ~100 | SpawnConfig, ToolCall, ToolResult types |
| `tool_executor.go` | ~80 | ToolExecutor interface |
| `event.go` | ~60 | Event types for streaming |

### `internal/ipc/grpcipc/`

| File | LOC | Role |
|------|-----|------|
| `worker_server.go` | ~150 | gRPC server (worker side) |
| `worker_client.go` | ~120 | gRPC client (control plane side) |

**Proto**: `internal/ipc/proto/hive.proto` → generated `hive.pb.go` (1518 LOC) + `hive_grpc.pb.go` (279 LOC).

**Tests**: `grpcipc_test.go` uses bufconn for in-memory gRPC testing.

---

## 7. Clustering (`internal/cluster/`)

The newest major subsystem. Leader/worker topology over gRPC with mTLS.

| File | LOC | Role |
|------|-----|------|
| `filesync.go` | 723 | Bidirectional file sync (atomic writes, streaming tar extraction) |
| `relay.go` | 399 | NAT traversal relay for workers behind firewalls |
| `leader_service.go` | 351 | gRPC service: worker registration, heartbeats, tool dispatch |
| `discovery.go` | 332 | Tracker-based discovery (register, discover, heartbeat) |
| `worker_stream.go` | 287 | Worker-side: connects to leader, handles tool call stream |
| `node_bridge.go` | 284 | Bridges remote cluster workers into local Manager |
| `remote_worker.go` | ~150 | RemoteWorker — wraps gRPC stream as `ipc.AgentWorker` |
| `leader_stream.go` | ~150 | Leader-side stream management |
| `registry.go` | ~120 | In-memory worker registry (connected nodes, capabilities) |
| `tls.go` | ~100 | mTLS cert generation and verification |
| `identity.go` | ~80 | Persistent node identity (UUID + keypair) |

**Tests**: `discovery_test.go`, `filesync_test.go`, `filesync_stress_test.go`, `identity_test.go`, `registry_test.go`, `remote_worker_test.go`, `stream_test.go`, `tls_test.go`.

---

## 8. Transport (`internal/transport/`)

Wire protocol for leader ↔ worker WebSocket communication.

| File | LOC | Role |
|------|-----|------|
| `server.go` | 415 | WebSocket server with auth, routing, connection lifecycle |
| `client.go` | ~200 | WebSocket client with reconnect logic |
| `protocol.go` | ~150 | JSON envelope types, message framing |

**Tests**: `transport_test.go`.

---

## 9. HTTP API & WebSocket (`internal/api/`)

| File | LOC | Role |
|------|-----|------|
| `server.go` | 363 | Router setup, middleware, static file serving, CORS |
| `chat.go` | 304 | WebSocket chat handler — message relay to/from coordinator |
| `files.go` | 490 | File browser API (list, read, write, rename, delete) |
| `share.go` | 236 | Conversation sharing (export/import) |
| `settings.go` | ~120 | Settings API (theme, model preferences) |
| `setup.go` | ~100 | First-run setup flow (API key, provider) |
| `auth.go` | ~80 | Auth middleware, session management |
| `terminal.go` | ~80 | Terminal WebSocket (PTY-backed) |
| `usage.go` | ~80 | Usage/cost reporting endpoints |

### REST Endpoints

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /api/health` | No | Health check |
| `GET /api/auth/status` | No | Auth state (needsSetup, authRequired, authenticated) |
| `POST /api/auth/login` | No | Login (rate-limited, bcrypt, sets cookie) |
| `POST /api/auth/logout` | Yes | Logout (clears cookie) |
| `POST /api/auth/password` | Yes | Change password (invalidates sessions) |
| `POST /api/setup` | No | First-run setup (CSRF-protected) |
| `GET/PUT /api/settings` | Yes | Default provider/model |
| `GET/PUT/DELETE /api/settings/providers/{type}` | Yes | Provider CRUD |
| `GET /api/instances` | Yes | List instances (optional mode filter) |
| `GET /api/instances/{id}/messages` | Yes | Conversation history |
| `POST /api/instances/{id}/start\|stop\|clear` | Yes | Instance lifecycle (root-protected) |
| `DELETE /api/instances/{id}` | Yes | Delete instance (root-protected) |
| `GET /api/usage[/models\|/daily]` | Yes | Token/cost analytics |
| `GET/PUT/DELETE /api/files/*` | Yes | File browser CRUD |
| `POST /api/files/share` | Yes | Create share token |
| `GET /api/shared/{token}[/raw]` | No | View shared file (token auth) |
| `WS /ws/chat` | Cond. | WebSocket chat to coordinator |
| `WS /ws/terminal` | Yes | WebSocket PTY terminal |

**Tests**: `server_test.go` (health, 404), `auth_test.go` (12 tests: login, logout, rate limiter, bearer token, password change, middleware), `files_test.go` (~40 subtests: tree, read, write, mkdir, delete, rename, path traversal), `instances_test.go` (8 tests: list, filter, root protection, messages), `settings_test.go` (5 tests: CRUD, singleton prevention), `usage_test.go` (5 tests: total, model, daily, no-DB, auth).

---

## 10. Control Plane (`internal/controlplane/`)

Operator-level config management — auth, providers, secrets, tool policies, clustering. Split into focused files (was 631 LOC monolith).

| File | LOC | Role |
|------|-----|------|
| `controlplane.go` | 211 | Core types (Config, ControlPlane), Load/Save/Reload, initMaps |
| `controlplane_auth.go` | 84 | NeedsSetup, PasswordHash, SetPasswordHash, TokenSigner |
| `controlplane_providers.go` | 160 | Provider CRUD, ProviderInfo, maskKey, default resolution |
| `controlplane_secrets.go` | 45 | SecretNames, SecretEnv, SetSecret, DeleteSecret |
| `controlplane_policies.go` | 42 | AgentTools, SetAgentTools, ClearAgentTools, AllPolicies |
| `controlplane_cluster.go` | 117 | ClusterMode, join token CRUD, ValidateJoinToken, env var overrides |
| `commands.go` | 259 | Slash command parsing (`/secrets`, `/tools`, `/cluster`) |

**Tests**: `controlplane_test.go` (46 tests covering auth, providers, secrets, policies, cluster, commands, reload, error paths).

---

## 11. Supporting Packages

| Package | File | LOC | Purpose |
|---------|------|-----|---------|
| `auth` | `auth.go` | ~100 | Token-based auth, session management |
| `hub` | `hub.go` | ~120 | Swarm worker tracking, skill-based dispatch |
| `uidpool` | `pool.go` | ~120 | Pre-allocated UID pool for process isolation |
| `watcher` | `watcher.go` | 344 | File system watcher (fsnotify), debounced change events |
| `models` | `models.go` | ~100 | Shared model types |
| `platform` | `init.go` | ~80 | Platform directory initialization |

**Tests**: All have corresponding test files.

---

## 12. Web UI (`web/ui/`)

React 19 + Vite + TypeScript + Tailwind + shadcn/ui.

### Pages

| File | Purpose |
|------|---------|
| `App.tsx` | Root layout, routing, auth gate |
| `pages/FilesPage.tsx` | File browser with editor |
| `pages/SharedFilePage.tsx` | View shared conversations |
| `pages/TerminalPage.tsx` | Web terminal (PTY) |

### Components

| Component | Purpose |
|-----------|---------|
| `Chat.tsx` | Main chat interface — message rendering, input, streaming |
| `Sidebar.tsx` | Instance list, navigation |
| `ActivityBar.tsx` | Left icon bar (chat, files, terminal, settings) |
| `FileTree.tsx` | File browser tree view |
| `FileEditor.tsx` | Code editor panel |
| `ModelSelector.tsx` | LLM model picker |
| `TokenCounter.tsx` | Usage display |
| `Login.tsx` | Auth form |
| `Settings.tsx` | User preferences panel |
| `Setup.tsx` | First-run onboarding |

### prompt-kit (chat rendering)

| Component | Purpose |
|-----------|---------|
| `chat-container.tsx` | Scrollable chat viewport |
| `markdown.tsx` | Markdown renderer with syntax highlighting |
| `code-block.tsx` | Code blocks with copy button |
| `prompt-input.tsx` | Chat input with submit |
| `scroll-button.tsx` | Scroll-to-bottom indicator |
| `loader.tsx` | Streaming/thinking indicator |

### Hooks & Utilities

| File | Purpose |
|------|---------|
| `hooks/use-websocket.ts` | WebSocket connection management, message state |
| `hooks/use-files.ts` | File tree data fetching |
| `hooks/use-file-watch.ts` | Live file change notifications |
| `hooks/use-theme.ts` | Dark/light theme toggle |
| `lib/chat-parser.ts` | Parse streaming WebSocket messages into chat state |
| `lib/chat-types.ts` | TypeScript types for chat protocol |
| `lib/file-utils.ts` | File path helpers |
| `lib/format.ts` | Number/date formatting |
| `lib/session-utils.ts` | Session management helpers |
| `lib/utils.ts` | General utilities (cn, etc.) |

### UI primitives (`components/ui/`)

shadcn/ui components: badge, button, card, dialog, dropdown-menu, input, label, scroll-area, select, separator, tabs, textarea, tooltip.

---

## 13. Testing Infrastructure

### Test Distribution

| Area | Test Files | Coverage Focus |
|------|-----------|----------------|
| `agent/tools/` | 14 files | Every built-in tool, real file/process ops |
| `inference/` | 9 files | Assembly, compaction, context, prompt, tools, redaction |
| `cluster/` | 8 files | Discovery, filesync, identity, registry, streams, TLS |
| `agent/` | 5 files | Manager, spawn, isolation (3 Docker-only) |
| `platform/db/` | 2 files | Schema, CRUD, FTS, cascades, usage, instance lifecycle |
| `api/` | 9 files | Auth, instances, settings, usage, files, server, setup, origin, share (101 tests) |
| `tests/e2e/` | 8 files | Full-stack: agents, chat, history, memory, todos, lifecycle |
| `tests/e2e_cluster/` | 1 file | Cluster integration |
| Other | 11 files | Config, controlplane, transport, hub, auth, etc. |

### Test Modes

| Command | Environment | What it tests |
|---------|-------------|---------------|
| `make test` | Docker | All unit + integration (mock workers) |
| `make test-local` | Local | Same, no Docker needed |
| `make test-isolation` | Docker (root) | UID isolation, permissions |
| `make test-online` | Local + API key | Real LLM calls |
| `make test-cluster` | Docker Compose | Multi-node cluster |
| `make test-cluster-relay` | Docker Compose | Cluster with relay |

---

## 14. Build & Deploy

| File | Purpose |
|------|---------|
| `Makefile` | All build/test/deploy targets |
| `Dockerfile` | Production image |
| `Dockerfile.testing` | Test image with user pool |
| `docker-compose.yml` | Single-node dev |
| `docker-compose.cluster.yml` | Multi-node cluster dev |
| `docker-compose.cluster-relay.yml` | Cluster with relay |
| `docker-compose.e2e.yml` | E2E test environment |

---

## 15. Capability Map — Quality Review Checklist

Each row is a reviewable unit. Tackle them in any order.

| # | Capability | Key Files | LOC | Tests | Notes |
|---|-----------|-----------|-----|-------|-------|
| 1 | **Agent Manager** | `agent/manager*.go` | ~1820 (8 files) | `manager_test.go` | Split into 8 focused files. Lifecycle, session, query, worker, resolve, restore. |
| 2 | **Inference Loop** | `inference/loop.go` | 564 | (integration) | Core agentic loop, streaming, tool dispatch. |
| 3 | **Compaction** | `inference/compaction.go` | 675 | `compaction_test.go` | LLM-driven summarization. Complex async logic. |
| 4 | **Local Tools** | `inference/tools_*.go` | ~606 (5 files) | `tools_test.go` | Split: spawn, memory, todos, history, skills. |
| 5 | **System Prompt** | `inference/prompt.go`, `assembly.go`, `context.go` | ~580 | 3 test files | Prompt assembly, token budgeting, context management. |
| 6 | **File Sync** | `cluster/filesync.go` | 723 | 2 test files | Bidirectional sync, atomic writes, streaming tar. |
| 7 | **Cluster Leader** | `cluster/leader_service.go`, `leader_stream.go` | ~500 | `stream_test.go` | gRPC service, worker registration, tool dispatch. |
| 8 | **Cluster Worker** | `cluster/worker_stream.go`, `node_bridge.go` | ~570 | `stream_test.go` | Worker connection, remote→local tool bridging. |
| 9 | **Relay** | `cluster/relay.go` | 399 | (manual) | NAT traversal relay server. |
| 10 | **Discovery** | `cluster/discovery.go` | 332 | `discovery_test.go` | Tracker registration, heartbeat, node lookup. |
| 11 | **Transport** | `transport/server.go`, `client.go`, `protocol.go` | ~765 | `transport_test.go` | WebSocket wire protocol, reconnect, auth. |
| 12 | **Control Plane** | `controlplane/*.go` (7 files) | ~918 | `controlplane_test.go` (53 tests) | Split into 7 focused files. Auth, providers, secrets, policies, cluster, commands. |
| 13 | **HTTP API** | `api/server.go`, `chat.go` | ~667 | 9 test files (101 tests) | REST routes, WebSocket chat, middleware, auth, settings, usage, setup, share. |
| 14 | **File Browser API** | `api/files.go` | 490 | `files_test.go` | List/read/write/rename/delete files. |
| 15 | **Terminal** | `api/terminal.go` | ~80 | — | PTY WebSocket. |
| 16 | **Auth** | `api/auth.go`, `auth/auth.go` | ~230 | `auth_test.go` (12 tests) | Token auth, sessions, rate limiter, password change. |
| 17 | **Database** | `platform/db/*.go` | ~1500 | 2 test files (18 tests) | Schema, messages, instances, sessions, usage, FTS. |
| 18 | **Config Parsing** | `config/markdown.go` | 394 | `markdown_test.go` | YAML frontmatter + markdown, agent/skill loading. |
| 19 | **Worker Spawn** | `agent/spawn.go` | ~180 | `spawn_test.go` | Process exec, UID switching, stdin pipe. |
| 20 | **IPC/gRPC** | `ipc/`, `ipc/grpcipc/` | ~750 | `grpcipc_test.go` | Interfaces, proto, gRPC adapters (bufconn tests). |
| 21 | **Bash Tool** | `agent/tools/bash.go`, `background.go` | ~440 | 3 test files | Shell exec, job management, auto-background. |
| 22 | **Search Tools** | `agent/tools/grep.go`, `glob.go` | ~624 | 2 test files | Ripgrep integration, Go fallbacks. |
| 23 | **Edit Tools** | `agent/tools/edit.go`, `multiedit.go` | ~330 | 2 test files | Find-and-replace, batch edits. |
| 24 | **File Tools** | `agent/tools/read_file.go`, `write_file.go`, `list_files.go` | ~340 | 3 test files | Read/write/list with sandboxing. |
| 25 | **UID Pool** | `uidpool/pool.go` | ~120 | `pool_test.go` | UID allocation for process isolation. |
| 26 | **File Watcher** | `watcher/watcher.go` | 344 | `watcher_test.go` | fsnotify wrapper, debounced events. |
| 27 | **Web UI: Chat** | `components/Chat.tsx`, `prompt-kit/*` | ~1500 | — | Chat interface, markdown, streaming. |
| 28 | **Web UI: Files** | `pages/FilesPage.tsx`, `FileTree.tsx`, `FileEditor.tsx` | ~800 | — | File browser, editor, tree. |
| 29 | **Web UI: Core** | `App.tsx`, `Sidebar.tsx`, `ActivityBar.tsx` | ~600 | — | Layout, routing, navigation. |
| 30 | **Web UI: Hooks** | `hooks/*`, `lib/*` | ~800 | — | WebSocket, file watch, chat parsing, state. |
| 31 | **E2E Tests** | `tests/e2e/*.go` | ~1000 | — | Full-stack integration tests in Docker. |
| 32 | **Cluster E2E** | `tests/e2e_cluster/` | ~200 | — | Multi-node cluster tests. |
| 33 | **Sharing** | `api/share.go` | 236 | `share_test.go` | Conversation export/import. Encrypt/decrypt roundtrip, create, access. |
| 34 | **Setup/Onboarding** | `api/setup.go`, `components/Setup.tsx` | ~200 | `setup_test.go` | First-run flow. Validation, CSRF, already-complete guard. |
| 35 | **Settings** | `api/settings.go`, `components/Settings.tsx` | ~240 | — | User preferences. |
| 36 | **Usage Tracking** | `api/usage.go`, `platform/db/usage.go` | ~314 | `db_test.go` | Token/cost aggregation. |

---

## 16. Hotspots — Where Complexity Lives

Files over 500 LOC or with high cyclomatic complexity deserve the most attention:

| File | LOC | Why it's hot |
|------|-----|-------------|
| `cluster/filesync.go` | 723 | Complex streaming + atomic writes + watcher coordination |
| `inference/compaction.go` | 682 | Async LLM calls + locking + tree summarization |
| `platform/db/messages.go` | 613 | Message storage + FTS + summary hierarchy |
| `inference/loop.go` | 564 | Core loop — streaming, error recovery, tool dispatch |
| `api/files.go` | 490 | File CRUD — path traversal security surface |
| `agent/manager_lifecycle.go` | 449 | Instance creation, spawning, shutdown — largest agent file post-split |
| `transport/server.go` | 415 | WebSocket lifecycle, auth, routing |
| `agent/manager_session.go` | 415 | Session management, config push |
| `cmd/hive/main.go` | 406 | Bootstrap — could be cleaner |
| `cluster/relay.go` | 399 | NAT traversal — network complexity |
| `config/markdown.go` | 394 | Parser — correctness matters |
| `agent/tools/grep.go` | 368 | Ripgrep + fallback — two code paths |
| `api/server.go` | 363 | Router setup, middleware stack |
| `cluster/leader_service.go` | 351 | gRPC service with stream management |
| `watcher/watcher.go` | 344 | fsnotify debouncing, recursive watching |
| `cluster/discovery.go` | 332 | HTTP-based tracker protocol |
| `cmd/hive/worker_node.go` | 329 | Worker node connection lifecycle |

---

## 17. Quality Findings

Synthesized from deep-dive reviews of every package. Organized by priority.

### Structural — Code Organization

| Finding | Where | Impact |
|---------|-------|--------|
| ~~**manager.go is a god object**~~ | `agent/manager*.go` | **DONE** — Split into 8 focused files (155 LOC core + 7 modules). |
| ~~**local_tools.go packs 15+ tools**~~ | `inference/tools_*.go` | **DONE** — Split into 5 files by tool category. |
| ~~**controlplane.go mixes concerns**~~ | `controlplane/*.go` | **DONE** — Split into 7 focused files (211 LOC core + 6 modules). |
| **filesync.go does too much** (723 LOC) | `cluster/filesync.go` | Initial sync, incremental sync, conflict resolution, watcher management. Could split into modules. |
| **Cleanup logic duplicated** | `agent/manager_worker.go` | Worker cleanup appears in `cleanupWorker()`, `softStop()`, `removeInstance()`, and `watchWorker()`. Now colocated in one file. |
| **Resource limits scattered** | `agent/tools/*.go`, `inference/*.go` | File sizes, output caps, timeouts, token estimates spread across files as bare constants. Centralize into a config struct. |

### Correctness

| Finding | Where | Severity |
|---------|-------|----------|
| ~~**Fresh tail can overflow context**~~ | `inference/assembly.go` | **FIXED** — Tail capped at 80% of budget; shrinks from oldest end with slog warning. |
| **Multiedit partial failure** | `agent/tools/multiedit.go` | Medium — By design: applies as many edits as possible, reports failures. No rollback. |
| ~~**write_file not atomic**~~ | `agent/tools/write_file.go` | **FIXED** — Uses temp+rename via `atomicWriteFile()`. |
| ~~**Memory/todos not atomic**~~ | `config/memory.go`, `config/todos.go` | **FIXED** — Both use `atomicWrite()` (temp+rename). |
| ~~**Compaction depth unbounded**~~ | `inference/compaction.go` | **FIXED** — Added `MaxSummaryDepth` (scales 4–8 with context window). Condensation stops at limit. |
| ~~**Job ID collision**~~ | `agent/tools/background.go` | **FIXED** — Widened from 12-bit (4K) to 24-bit (16M) hex ID space. |
| ~~**HandleCommand swallows save errors**~~ | `controlplane/commands.go` | **FIXED** — Appends warning to result string when Save() fails. |
| ~~**hasContent misses JoinTokens**~~ | `controlplane/controlplane.go` | **FIXED** — Join tokens now included in hasContent(); were silently not persisted. |
| ~~**SetProvider lacks validation**~~ | `controlplane/controlplane_providers.go` | **FIXED** — Returns error for empty provider type or API key. Callers updated. |

### Security Surface

| Finding | Where | Severity |
|---------|-------|----------|
| **Path security is strong** | `api/files.go` | Positive — Defense-in-depth: lexical + symlink + TOCTOU checks. Well-tested. |
| **mTLS + join tokens** | `cluster/tls.go`, `discovery.go` | Positive — Constant-time token comparison, Ed25519 identity, cert pinning. |
| **Secret redaction** | `inference/redact.go` | Positive — Sorts by length desc, min 8-byte guard. |
| ~~**resolve.go doesn't EvalSymlinks**~~ | `agent/tools/resolve.go` | **FIXED** — EvalSymlinks added; rejects paths that resolve outside roots via symlink. |
| ~~**API key masking revealed short keys**~~ | `controlplane/controlplane_providers.go` | **FIXED** — Raised threshold so keys ≤10 chars are fully masked. |
| **SSRF protection opt-in** | `agent/tools/fetch.go` | Low — Must call `SetSSRFProtection(true)` explicitly. Should default to true. |
| **Relay status bytes unauthenticated** | `cluster/relay.go` | Low — MITM could inject false status. Mitigated by mTLS on the data path. |
| **Rate limiter ignores reverse proxy** | `api/auth.go` | Medium — Keys on `RemoteAddr`; behind a proxy, all clients share the same key. Needs trusted-proxy header extraction. |
| **Setup CSRF vulnerable to DNS rebinding** | `api/server.go` | Medium — `isSameOrigin` accepts matching Origin/Host from any domain. Restrict to loopback during setup. |
| **Password change silently logs out user** | `api/auth.go` | Medium — Session secret rotated but no new token issued in response. |

### Concurrency

| Finding | Where | Notes |
|---------|-------|-------|
| **Lock hierarchy well-documented** | `agent/manager.go` | `m.mu → inst.mu` ordering prevents deadlocks. |
| **Dual compaction locks** | `inference/loop.go` | `updateMu` (fast config) + `compactMu` (slow DB) — good design. |
| ~~**TokenSigner write-locks on hot path**~~ | `controlplane/controlplane_auth.go` | **FIXED** — Read-lock fast path with double-checked write-lock upgrade. |
| **allowedRoots is a global** | `agent/tools/resolve.go` | Set once, never guarded. Should be immutable or mutex-protected. |
| **No gRPC flow control** | `cluster/leader_stream.go` | Can queue unlimited messages before receiver drains. No `MaxConcurrentStreams` set. (TODO) |
| ~~**Unbounded handler goroutines**~~ | `cluster/worker_stream.go` | **FIXED** — Added semaphore (64 concurrent handlers max). |
| **No recv timeout on streams** | `cluster/leader_stream.go` | Hung nodes block the readLoop indefinitely. (TODO — requires heartbeat-based liveness) |

### Testing Gaps

| Gap | Where | Recommendation |
|-----|-------|----------------|
| ~~**API endpoints largely untested**~~ | `api/` | **DONE** — 101 tests across 9 files covering auth, instances, settings, usage, files, setup, share, origin. Remaining gaps: chat WebSocket, terminal WebSocket. |
| ~~**Control plane providers/cluster untested**~~ | `controlplane/` | **DONE** — 25 → 53 tests. Providers, cluster commands/config, auth getters, env overrides, error paths. |
| **No CI/CD pipeline** | Project-wide | All testing is manual via Makefile. Add GitHub Actions. |
| **No integration tests for manager lifecycle** | `agent/manager_test.go` | Mock worker is simplistic. Test full create→send→stop→restore flow. |
| **No concurrency stress tests for inference** | `inference/` | Model switch during Chat(), concurrent SendMessage. |
| **Cluster tests require live services** | `tests/e2e_cluster/` | Depends on `discover.hellohiro.ai` and relay. Add mock tracker option. |
| **Frontend has zero tests** | `web/ui/` | No unit or component tests. |

### Web UI

| Finding | Severity | Notes |
|---------|----------|-------|
| **Strong type safety** | Positive | Strict TS, well-defined protocol types, generic architecture. |
| **Good component design** | Positive | Hooks for logic, compound components, ref-based APIs. |
| **No toast/notification system** | Medium | Share/upload/error feedback is ad-hoc. Needs centralized toast. |
| **Error recovery shows console.error only** | Medium | `handleStop`, `handleStart`, `handleDelete` don't show user-facing errors. |
| **No message virtualization** | Low | Long conversations load all messages. Could lag with 1000+ messages. |
| **No mobile responsiveness** | Low | Layout not optimized for small screens. |
| **Base64 attachments in memory** | Low | Many image attachments impact performance. |

### Database

| Finding | Notes |
|---------|-------|
| **Schema design is solid** | Proper FKs, CHECK constraints, FTS5, WAL mode, cascade deletes. |
| **FTS design is clever** | External content for integer-PK tables, standalone for text-PK. Trigger-based sync. |
| **No VACUUM/optimize strategy** | WAL can grow large. Consider periodic `PRAGMA optimize`. |
| **Single reader bottleneck** | `MaxOpenConns=1` serializes reads. Fine for now, bottleneck under high analytics load. |

---

## 18. Suggested Review Order

Completed items struck through. Next priorities:

1. ~~**Split `manager.go`**~~ — **DONE** (8 files, 155 LOC core).
2. ~~**Inference correctness**~~ — **DONE** (fresh tail overflow fix, compaction depth cap).
3. ~~**Split `local_tools.go`**~~ — **DONE** (5 files by tool category).
4. ~~**Cluster hardening**~~ — **DONE** (path traversal fix, node bridge robustness, goroutine bounding). Remaining: recv timeouts, gRPC flow control.
5. ~~**File sync**~~ — **DONE** (Reconcile wired into production after initial sync; watch race documented as sufficient).
6. ~~**Tool correctness**~~ — **DONE** (atomic writes for write_file/memory/todos, resolve.go symlink protection, job ID space widened).
7. ~~**API test coverage**~~ — **DONE** (2 → 101 tests across 9 files: auth, instances, settings, usage, files, setup, share, origin). Remaining: chat/terminal WebSocket.
8. **Web UI polish** — Toast system, error display, virtualization.
9. ~~**Control plane cleanup**~~ — **DONE** (split into 7 files, provider validation, save error surfacing, hasContent/JoinTokens fix, maskKey hardening, TokenSigner lock optimization, 25 → 53 tests). Remaining: rate limiter proxy support, setup CSRF hardening, password change session reissue.
