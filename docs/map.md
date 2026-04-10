# Hiro Project Map

> Comprehensive map of every major capability, package, and subsystem.
> Use this to plan quality improvements one piece at a time.

## At a Glance

| Metric | Value |
|--------|-------|
| Go source (non-test) | ~26.6k LOC across 122 files |
| Go test code | ~34.3k LOC across 107 test files |
| Frontend (TS/TSX) | ~9.0k LOC across 59 files |
| Internal packages | 17 |
| Top-level commands | 4 (`main.go`, `bootstrap.go`, `agent.go`, `worker_node.go`) |

---

## 1. Entry Points (`cmd/hiro/`)

| File | LOC | Role |
|------|-----|------|
| `main.go` | 584 | CLI parsing, `run()` starts control plane (HTTP, manager, DB) |
| `bootstrap.go` | 195 | Cluster setup helpers: `setupNodeIdentity`, `setupClusterServer`, `bootstrapOperator` |
| `agent.go` | 236 | Agent worker subprocess — reads SpawnConfig from stdin, serves gRPC |
| `worker_node.go` | 496 | Worker node for cluster mode — connects to leader, bridges remote tool calls |

**Bootstrap flow**: parse flags → load `.env` → open DB → `setupNodeIdentity` → `setupClusterServer` → create Manager → `bootstrapOperator` → start HTTP server.

---

## 2. Agent Management (`internal/agent/`)

The core of instance lifecycle management. Split into focused files (was 1,742 LOC monolith).

| File | LOC | Role |
|------|-----|------|
| `manager.go` | 164 | Core types (Manager, instance, InstanceInfo, WorkerHandle), constructor, registry primitives |
| `manager_lifecycle.go` | 611 | CreateInstance, SpawnEphemeral, StopInstance, DeleteInstance, startInstance, Shutdown |
| `manager_session.go` | 511 | StartInstance (restart), NewSession, UpdateInstanceConfig (model/reasoning to filesystem), agent definition config push/watch |
| `manager_query.go` | 278 | GetInstance, ListInstances, GetHistory, InstanceByAgentName, IsDescendant |
| `manager_worker.go` | 249 | shutdownHandle, cleanupWorker, softStop, watchWorker, removeInstance |
| `manager_resolve.go` | 327 | computeEffectiveTools, buildAllowedToolsMap, tool rules, resolveProvider/Model/Credentials |
| `manager_helpers.go` | 213 | SendMessage, SecretNames/Env, path helpers, validateAgentName |
| `manager_restore.go` | 194 | RestoreInstances (startup recovery from DB) |
| `agent.go` | 9 | Options struct (provider code extracted to `internal/provider`) |
| `spawn.go` | 217 | Worker process spawning (exec, UID switching, stdin pipe) |
| `tool_executor.go` | 48 | ToolExecutor adapter — dispatches tool calls to local fantasy.AgentTool by name |

### `internal/agent/tools/` — Built-in Tool Implementations

All run in **worker processes**, dispatched via gRPC.

| File | LOC | Tool |
|------|-----|------|
| `bash.go` | 172 | Shell execution, 120s timeout, auto-background |
| `background.go` | 265 | Background job registry (start, poll, kill) |
| `grep.go` | 712 | Regex search with ripgrep fallback to Go, pagination, output modes |
| `glob.go` | 256 | File pattern matching, ripgrep or Go fallback |
| `edit.go` | 128 | Surgical find-and-replace |
| `read.go` | 82 | Read with offset/limit, 64KB cap |
| `write.go` | 49 | Atomic file write (temp+rename), auto-mkdir |
| `webfetch.go` | 122 | HTTP fetch, 64KB response cap |
| `task_output.go` | 72 | Background task stdout/stderr |
| `task_stop.go` | 46 | Terminate background task |
| `schema.go` | 36 | `RemoteToolNames` registry + `RemoteToolInfos()` for schema extraction |
| `resolve.go` | 143 | Path resolution, sandboxing, symlink confinement, atomicWriteFile |
| `limits.go` | 109 | Centralized resource limit constants (timeouts, output sizes, buffer limits) |
| `rg.go` | 55 | Ripgrep detection helper |

**Tests**: Every tool has a corresponding `*_test.go`. Tests run real file/process ops in temp dirs.

---

## 3. Inference Engine (`internal/inference/`)

Runs in the **control plane process**. Drives the agentic loop per instance.

| File | LOC | Role |
|------|-----|------|
| `loop.go` | 887 | Main inference loop — calls `fantasy.Agent.Stream()`, handles tool dispatch |
| `tools_spawn.go` | 431 | Spawn tool + management tools (ScopedManager, ListNodes, SendMessage, etc.) |
| `tools_memory.go` | 141 | AddMemory, ForgetMemory tools |
| `tools_history.go` | 148 | HistorySearch, HistoryRecall |
| `tools_skills.go` | 158 | Skill + path validation |
| `tools_todos.go` | 86 | TodoWrite tool |

| `compaction.go` | 777 | LLM-driven conversation summarization (now with MaxSummaryDepth cap) |
| `context_providers.go` | 441 | Context provider system — delta-tracked system reminders (memories, todos, secrets, skills, agents) |
| `assembly.go` | 179 | Message assembly within token budget (now with fresh tail overflow protection) |
| `context.go` | 46 | Context item types and helpers |
| `prompt.go` | 110 | System prompt builder (persona + agent.md + environment) |
| `notifications.go` | 101 | Background task completion notifications |
| `tools.go` | 119 | Tool proxy — wraps remote tool schemas (from `tools.RemoteToolInfos`) with gRPC dispatch |
| `helpers.go` | 257 | Token counting, message utilities |
| `redact.go` | 55 | Secret redaction in tool outputs |
| `tool.go` | 35 | Tool type definitions |

### Local Tools (run in control plane, not workers)

| Tool | Scope | Purpose |
|------|-------|---------|
| `SpawnInstance` | All agents | Spawn child instance (ephemeral/persistent) |
| `AddMemory` / `ForgetMemory` | Persistent+ | Manage persistent memory entries |
| `TodoWrite` | Persistent+ | Manage task list (YAML) |
| `HistorySearch` / `HistoryRecall` | Persistent+ | FTS search conversation history |
| `ResumeInstance` | Operator | Restart stopped child |
| `SendMessage` | Operator | Message child, get response |
| `StopInstance` | Operator | Stop child + subtree |
| `DeleteInstance` | Operator | Permanently remove child + subtree |
| `ListInstances` | Operator | List direct children |
| `Skill` | Agents with skills | Load skill instructions on demand |

**Tests**: 17 test files — `assembly_test.go`, `compaction_test.go`, `context_test.go`, `helpers_test.go`, `notifications_test.go`, `prompt_test.go`, `redact_test.go`, `tools_test.go`, `tools_history_test.go`, `tools_management_test.go`, `tools_memory_test.go`, `tools_skills_test.go`, `tools_spawn_test.go`, `tools_todos_test.go`, plus online eval tests (`compaction_online_test.go`, `eval_code_test.go`, `eval_locomo_test.go`).

---

## 4. Database (`internal/platform/db/`)

Unified SQLite database (`db/hiro.db`). Single writer, WAL mode, FTS5 for search. Pure Go SQLite via `modernc.org/sqlite`.

| File | LOC | Role |
|------|-----|------|
| `messages.go` | 637 | Message CRUD, FTS indexing, summary storage, context assembly queries |
| `instances.go` | 252 | Instance CRUD, parent-child relationships |
| `sessions.go` | 241 | Session lifecycle, cascade deletes |
| `usage.go` | 234 | Token/cost usage tracking and aggregation |
| `logs.go` | 195 | Structured log storage, querying, streaming |
| `db.go` | 152 | Schema, migrations, connection setup, WAL config |

**Schema entities**: instances, sessions, messages, summaries, context_items, usage_events, request_log, logs.

**Tests**: 6 test files — `db_test.go`, `instances_test.go`, `sessions_test.go`, `usage_test.go`, `logs_test.go`, plus integration tests.

---

## 5. Config & Parsing (`internal/config/`)

| File | LOC | Role |
|------|-----|------|
| `markdown.go` | 445 | YAML frontmatter + markdown parser, agent/skill config loading |
| `persona.go` | 109 | Persona.md read/write helpers, seeding from agent definition |
| `todos.go` | 75 | Todos YAML read/write helpers (atomic write) |
| `memory.go` | 56 | Memory.md read/write helpers (atomic write) |

**Tests**: `markdown_test.go`, `memory_test.go`, `persona_test.go`, `todos_test.go`, `stringslice_test.go`.

---

## 6. IPC & gRPC (`internal/ipc/`)

Interfaces and types for control plane ↔ worker communication.

| File | LOC | Role |
|------|-----|------|
| `host_manager.go` | 76 | `HostManager` interface (inference→manager callbacks), `NodeID` type, `HomeNodeID` constant |
| `types.go` | 45 | SpawnConfig, ToolCall, ToolResult types |
| `event.go` | 26 | Event types for streaming |
| `worker.go` | 19 | `AgentWorker` interface (ExecuteTool + Shutdown), `SecretEnvSetter` optional interface |
| `tool_executor.go` | 17 | ToolExecutor interface |

### `internal/ipc/grpcipc/`

| File | LOC | Role |
|------|-----|------|
| `worker_client.go` | 89 | gRPC client (control plane side) |
| `worker_server.go` | 87 | gRPC server (worker side) |

**Proto**: `internal/ipc/proto/hiro.proto` → generated `hiro.pb.go` (1518 LOC) + `hiro_grpc.pb.go` (279 LOC).

**Tests**: `grpcipc_test.go` uses bufconn for in-memory gRPC testing.

---

## 7. Clustering (`internal/cluster/`)

The newest major subsystem. Leader/worker topology over gRPC with mTLS.

| File | LOC | Role |
|------|-----|------|
| `leader_service.go` | 446 | gRPC service: worker registration, heartbeats, tool dispatch |
| `leader_stream.go` | 437 | Leader-side stream management |
| `relay.go` | 413 | NAT traversal relay for workers behind firewalls |
| `discovery.go` | 394 | Tracker-based discovery (register, discover, heartbeat) |
| `worker_stream.go` | 391 | Worker-side: connects to leader, handles tool call stream |
| `node_bridge.go` | 358 | Bridges remote cluster workers into local Manager |
| `filesync_incremental.go` | 287 | `WatchAndSync`, `sendChange`, `ApplyFileUpdate`, echo suppression |
| `worker_terminal.go` | 226 | Remote PTY/terminal session handling over gRPC |
| `filesync_util.go` | 211 | `Reconcile`, `addWatchRecursive`, `scanNewDir`, atomic write helpers |
| `registry.go` | 194 | In-memory worker registry (connected nodes, capabilities) |
| `pending.go` | 180 | Node approval state management (pending, approved, revoked) |
| `remote_worker.go` | 159 | RemoteWorker — wraps gRPC stream as `ipc.AgentWorker` |
| `filesync_initial.go` | 155 | `CreateInitialSync`, `ApplyInitialSyncStream` (tar create/extract) |
| `tls.go` | 114 | mTLS cert generation and verification |
| `constants.go` | 89 | Cluster constants and configuration defaults |
| `filesync.go` | 82 | Core type, config, constructor, Stop |
| `identity.go` | 74 | Persistent node identity (UUID + keypair) |
| `filesync_filter.go` | 67 | Constants, ignore rules, `shouldIgnore`, `sanitizeNodeID` |
| `tokens.go` | 28 | Swarm code generation |

**Tests**: 20 test files — `discovery_test.go`, `filesync_test.go`, `filesync_stress_test.go`, `identity_test.go`, `pending_test.go`, `registry_test.go`, `relay_test.go`, `remote_worker_test.go`, `stream_test.go`, `tls_test.go`, `tokens_test.go`, `worker_terminal_test.go`, and others.

---

## 8. Transport (`internal/transport/`)

Wire protocol for leader ↔ worker WebSocket communication.

| File | LOC | Role |
|------|-----|------|
| `server.go` | 444 | WebSocket server with auth, routing, connection lifecycle |
| `client.go` | 199 | WebSocket client with reconnect logic |
| `protocol.go` | 73 | JSON envelope types, message framing |

**Tests**: `transport_test.go`.

---

## 9. HTTP API & WebSocket (`internal/api/`)

| File | LOC | Role |
|------|-----|------|
| `terminal_session.go` | 826 | Terminal session management (multi-session PTY lifecycle, resize, reconnect) |
| `files.go` | 555 | File browser API (list, read, write, rename, delete) |
| `server.go` | 528 | Router setup, middleware, static file serving; `NewServer(logger, webFS, cp, pdb, rootDir)` |
| `chat.go` | 479 | WebSocket chat handler — message relay to/from operator; `SetManager`, `SetStartManager`, `SetWatcher` |
| `setup.go` | 354 | First-run setup flow (API key, provider, swarm validation) |
| `auth.go` | 330 | Auth middleware, session management, rate limiting |
| `terminal.go` | 280 | Terminal WebSocket (PTY-backed) |
| `share.go` | 235 | Conversation sharing (export/import) |
| `settings.go` | 195 | Settings API (theme, model preferences, providers) |
| `cluster_nodes.go` | 163 | Cluster node management API (pending, approved, revoked) |
| `logs.go` | 152 | Log querying and streaming endpoints |
| `usage.go` | 145 | Usage/cost reporting endpoints |
| `cluster_settings.go` | 141 | Cluster settings API (reset, configuration) |
| `terminal_cluster.go` | 85 | Cluster terminal routing (remote PTY via gRPC) |
| `constants.go` | 26 | Shared API constants |

### REST Endpoints

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /api/health` | No | Health check |
| `GET /api/auth/status` | No | Auth state (needsSetup, authRequired, authenticated) |
| `POST /api/auth/login` | No | Login (rate-limited, bcrypt, sets cookie) |
| `POST /api/auth/logout` | Yes | Logout (clears cookie) |
| `POST /api/auth/password` | Yes | Change password (invalidates sessions) |
| `POST /api/setup` | No | First-run setup (CSRF-protected) |
| `POST /api/setup/test-provider` | No | Test provider connection during setup |
| `POST /api/setup/validate-swarm` | No | Validate swarm code during setup |
| `GET /api/setup/provider-types` | No | List available provider types |
| `GET /api/setup/models` | No | List available models for provider |
| `GET/PUT /api/settings` | Yes | Default provider/model |
| `GET /api/settings/cluster` | Yes | Cluster settings |
| `POST /api/settings/cluster/reset` | Strict | Reset cluster configuration |
| `GET /api/settings/providers` | Yes | List configured providers |
| `PUT/DELETE /api/settings/providers/{type}` | Yes | Provider CRUD |
| `POST /api/settings/providers/{type}/test` | Yes | Test provider connection |
| `GET /api/cluster/pending` | Strict | List pending node approvals |
| `POST /api/cluster/pending/{nodeID}/approve` | Strict | Approve pending node |
| `DELETE /api/cluster/pending/{nodeID}` | Strict | Dismiss pending node |
| `GET /api/cluster/approved` | Strict | List approved nodes |
| `DELETE /api/cluster/approved/{nodeID}` | Strict | Remove approved node |
| `DELETE /api/cluster/revoked/{nodeID}` | Strict | Clear revoked node |
| `GET /api/instances` | Yes | List instances (optional mode filter) |
| `GET /api/instances/{id}/messages` | Yes | Conversation history |
| `GET /api/instances/{id}/usage` | Yes | Per-instance usage stats |
| `POST /api/instances/{id}/start\|stop\|clear` | Yes | Instance lifecycle (root-protected) |
| `DELETE /api/instances/{id}` | Yes | Delete instance (root-protected) |
| `GET /api/sessions/{id}/messages` | Yes | Session conversation history |
| `GET /api/models` | Yes | List available models |
| `GET /api/provider-types` | Yes | List available provider types |
| `GET /api/usage[/models\|/daily]` | Yes | Token/cost analytics |
| `GET /api/logs` | Strict | Query structured logs |
| `GET /api/logs/stream` | Strict | Stream logs via SSE |
| `GET /api/logs/sources` | Strict | List log sources |
| `GET/PUT/DELETE /api/files/*` | Yes | File browser CRUD |
| `GET /api/files/events` | Yes | File change event stream |
| `POST /api/files/share` | Yes | Create share token |
| `GET /api/shared/{token}[/raw]` | No | View shared file (token auth) |
| `GET /api/terminal/sessions` | Yes | List terminal sessions |
| `GET /api/terminal/nodes` | Yes | List nodes available for terminal |
| `WS /ws/chat` | Cond. | WebSocket chat to operator |
| `WS /ws/terminal` | Yes | WebSocket PTY terminal |

**Tests**: 14 test files — `server_test.go`, `auth_test.go`, `files_test.go`, `instances_test.go`, `settings_test.go`, `usage_test.go`, `setup_test.go`, `share_test.go`, `origin_test.go`, `cluster_nodes_test.go`, `cluster_settings_test.go`, `terminal_session_test.go`, and others.

---

## 10. Control Plane (`internal/controlplane/`)

Operator-level config management — auth, providers, secrets, clustering. Split into focused files.

| File | LOC | Role |
|------|-----|------|
| `commands.go` | 167 | Slash command parsing (`/secrets`, `/cluster`) |
| `controlplane.go` | 258 | Core types (Config, ControlPlane), Load/Save/Reload, initMaps |
| `controlplane_cluster.go` | 205 | ClusterMode, join token CRUD, ValidateJoinToken, node approval, env var overrides |
| `controlplane_providers.go` | 152 | Provider CRUD, ProviderInfo, maskKey, default resolution |
| `controlplane_auth.go` | 89 | NeedsSetup, PasswordHash, SetPasswordHash, TokenSigner |
| `controlplane_secrets.go` | 54 | SecretNames, SecretEnv, SetSecret, DeleteSecret |

**Tests**: `controlplane_test.go` (53 tests covering auth, providers, secrets, policies, cluster, commands, reload, error paths).

---

## 11. Supporting Packages

| Package | File(s) | LOC | Purpose |
|---------|---------|-----|---------|
| `toolrules` | `checker.go`, `rule.go`, `bash.go`, `wildcard.go` | 662 | Tool permission rules engine — rule parsing, matching, Bash command filtering, wildcard patterns |
| `watcher` | `watcher.go` | 347 | File system watcher (fsnotify), debounced change events |
| `hub` | `hub.go` | 191 | Swarm worker tracking, skill-based dispatch |
| `models` | `models.go`, `modelspec.go` | 191 | Shared model types + model specification definitions |
| `platform` | `init.go` | 187 | Platform directory initialization |
| `provider` | `provider.go` | 174 | LLM provider construction (`CreateLanguageModel`, `TestConnection`, `AvailableProviders`). Imports all fantasy provider SDKs. |
| `platform/loghandler` | `handler.go` | 356 | Structured slog handler for platform-wide log capture |
| `auth` | `auth.go` | 118 | Token-based auth, session management |
| `landlock` | `landlock.go`, `landlock_other.go` | 110 | Landlock LSM filesystem restrictions (Linux-only, stubs on other platforms) |

**Tests**: All have corresponding test files.

---

## 12. Web UI (`web/ui/`)

React 19 + Vite + TypeScript + Tailwind + shadcn/ui.

### Pages

Organized by feature directory under `pages/`.

| Directory | Files | Purpose |
|-----------|-------|---------|
| `pages/chat/` | `ChatPage.tsx`, `ChatInput.tsx`, `ChatMessages.tsx`, `ModelSelector.tsx`, `Sidebar.tsx`, `TokenCounter.tsx` | Chat interface — message rendering, input, streaming, instance navigation |
| `pages/files/` | `FilesPage.tsx`, `FileTree.tsx`, `FileEditor.tsx` | File browser with editor |
| `pages/settings/` | `SettingsPage.tsx`, `ProvidersCard.tsx`, `DefaultModelCard.tsx`, `SecurityCard.tsx`, `ClusterCard.tsx` | Settings dashboard with provider management, security, clustering |
| `pages/terminal/` | `TerminalPage.tsx`, `TerminalInstance.tsx`, `TerminalTabBar.tsx` | Multi-session web terminal (PTY) |
| `pages/logs/` | `LogsPage.tsx` | Structured log viewer with filtering and streaming |
| `pages/shared/` | `SharedFilePage.tsx` | View shared conversations |

### Components

| Component | Purpose |
|-----------|---------|
| `App.tsx` | Root layout, routing, auth gate |
| `ActivityBar.tsx` | Left icon bar (chat, files, terminal, logs, settings) |
| `Login.tsx` | Auth form |
| `Setup.tsx` | First-run onboarding |
| `WorkerStatus.tsx` | Cluster worker status display |
| `ErrorBoundary.tsx` | React error boundary |

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
| `hooks/use-log-stream.ts` | Log streaming via SSE |
| `hooks/use-theme.ts` | Dark/light theme toggle |
| `lib/chat-parser.ts` | Parse streaming WebSocket messages into chat state |
| `lib/chat-types.ts` | TypeScript types for chat protocol |
| `lib/file-utils.ts` | File path helpers |
| `lib/format.ts` | Number/date formatting |
| `lib/session-utils.ts` | Session management helpers |
| `lib/utils.ts` | General utilities (cn, etc.) |

### UI primitives (`components/ui/`)

shadcn/ui components: badge, button, card, collapsible, dialog, dropdown-menu, input, label, popover, scroll-area, select, separator, skeleton, tabs, textarea, tooltip.

---

## 13. Testing Infrastructure

### Test Distribution

| Area | Test Files | Coverage Focus |
|------|-----------|----------------|
| `agent/tools/` | 14 files | Every built-in tool, real file/process ops |
| `inference/` | 17 files | Assembly, compaction, context, prompt, tools (per-category), notifications, redaction, online evals |
| `cluster/` | 20 files | Discovery, filesync, identity, pending, registry, relay, streams, TLS, tokens, terminal |
| `api/` | 14 files | Auth, instances, settings, usage, files, server, setup, origin, share, cluster nodes/settings, terminal sessions |
| `agent/` | 7 files | Manager, spawn, isolation (Docker-only) |
| `platform/db/` | 6 files | Schema, CRUD, FTS, cascades, usage, instances, sessions, logs |
| `tests/e2e/` | 9 files | Full-stack: agents, chat, history, memory, todos, lifecycle |
| `tests/e2e_cluster/` | 3 files | Cluster integration, background tasks, lifecycle |
| Other | 17 files | Config, controlplane, transport, hub, auth, models, toolrules, loghandler, etc. |

### Test Modes

| Command | Environment | What it tests |
|---------|-------------|---------------|
| `make test` | Docker | All unit + integration (mock workers) |
| `make test-local` | Local | Same, no Docker needed |
| `make test-online` | Local + API key | Real LLM calls |
| `make test-cluster` | Docker Compose | Multi-node cluster |
| `make test-cluster-relay` | Docker Compose | Cluster with relay |

---

## 14. Build & Deploy

| File | Purpose |
|------|---------|
| `Makefile` | All build/test/deploy targets |
| `Dockerfile` | Production image (also used for testing) |
| `docker-compose.yml` | Single-node dev |
| `docker-compose.cluster.yml` | Multi-node cluster dev |
| `docker-compose.cluster-relay.yml` | Cluster with relay |
| `docker-compose.e2e.yml` | E2E test environment |

---

## 15. Capability Map — Quality Review Checklist

Each row is a reviewable unit. Tackle them in any order.

| # | Capability | Key Files | LOC | Tests | Notes |
|---|-----------|-----------|-----|-------|-------|
| 1 | **Agent Manager** | `agent/manager*.go` | ~2550 (8 files) | `manager_test.go` + 6 more | Split into 8 focused files. Lifecycle, session, query, worker, resolve, restore. |
| 2 | **Inference Loop** | `inference/loop.go` | 887 | (integration) | Core agentic loop, streaming, tool dispatch. |
| 3 | **Compaction** | `inference/compaction.go` | 777 | `compaction_test.go` | LLM-driven summarization. Complex async logic. |
| 4 | **Local Tools** | `inference/tools_*.go` | ~964 (6 files) | 7 test files | Split: spawn, memory, todos, history, skills. |
| 5 | **System Prompt** | `inference/prompt.go`, `assembly.go`, `context.go`, `context_providers.go` | 776 | 3 test files | Prompt assembly, token budgeting, context provider system. |
| 6 | **File Sync** | `cluster/filesync*.go` | 802 (5 files) | 2 test files | Bidirectional sync, atomic writes, streaming tar. |
| 7 | **Cluster Leader** | `cluster/leader_service.go`, `leader_stream.go` | 883 | `stream_test.go` | gRPC service, worker registration, tool dispatch. |
| 8 | **Cluster Worker** | `cluster/worker_stream.go`, `node_bridge.go` | 749 | `stream_test.go` | Worker connection, remote→local tool bridging. |
| 9 | **Relay** | `cluster/relay.go` | 413 | `relay_test.go` | NAT traversal relay server. |
| 10 | **Discovery** | `cluster/discovery.go` | 394 | `discovery_test.go` | Tracker registration, heartbeat, node lookup. |
| 11 | **Transport** | `transport/server.go`, `client.go`, `protocol.go` | 716 | `transport_test.go` | WebSocket wire protocol, reconnect, auth. |
| 12 | **Control Plane** | `controlplane/*.go` (7 files) | 1149 | `controlplane_test.go` (53 tests) | Split into 7 focused files. Auth, providers, secrets, policies, cluster, commands. |
| 13 | **HTTP API** | `api/server.go`, `chat.go` | 1007 | 14 test files | REST routes, WebSocket chat, middleware, auth, settings, usage, setup, share, cluster, logs. |
| 14 | **File Browser API** | `api/files.go` | 555 | `files_test.go` | List/read/write/rename/delete files. |
| 15 | **Terminal** | `api/terminal.go`, `terminal_session.go`, `terminal_cluster.go` | 1191 | `terminal_session_test.go` | Multi-session PTY WebSocket, cluster routing. |
| 16 | **Auth** | `api/auth.go`, `auth/auth.go` | 448 | `auth_test.go` | Token auth, sessions, rate limiter, password change. |
| 17 | **Database** | `platform/db/*.go` | 1711 | 6 test files | Schema, messages, instances, sessions, usage, logs, FTS. |
| 18 | **Config Parsing** | `config/markdown.go`, `persona.go` | 554 | `markdown_test.go`, `persona_test.go` | YAML frontmatter + markdown, agent/skill/persona loading. |
| 19 | **Worker Spawn** | `agent/spawn.go` | 217 | `spawn_test.go` | Process exec, UID switching, stdin pipe. |
| 20 | **IPC/gRPC** | `ipc/`, `ipc/grpcipc/` | 359 | `grpcipc_test.go` | Interfaces, proto, gRPC adapters (bufconn tests). |
| 21 | **Bash Tool** | `agent/tools/bash.go`, `background.go` | 437 | 3 test files | Shell exec, job management, auto-background. |
| 22 | **Search Tools** | `agent/tools/grep.go`, `glob.go` | 968 | 2 test files | Ripgrep integration, Go fallbacks, pagination, output modes. |
| 23 | **Edit Tool** | `agent/tools/edit.go` | 128 | 1 test file | Find-and-replace. |
| 24 | **File Tools** | `agent/tools/read.go`, `write.go` | 131 | 2 test files | Read/write with sandboxing. |
| 25 | **Landlock** | `landlock/landlock.go` | 110 | — | Landlock LSM filesystem restrictions. Linux-only; stubs on other platforms. |
| 26 | **File Watcher** | `watcher/watcher.go` | 347 | `watcher_test.go` | fsnotify wrapper, debounced events. |
| 27 | **Tool Rules** | `toolrules/*.go` | 662 | `toolrules_test.go` | Tool permission rules engine, Bash command filtering, wildcards. |
| 28 | **Log Handler** | `platform/loghandler/handler.go` | 356 | `handler_test.go` | Structured slog handler for platform-wide log capture. |
| 29 | **Web UI: Chat** | `pages/chat/*.tsx`, `prompt-kit/*` | — | — | Chat interface, markdown, streaming. |
| 30 | **Web UI: Files** | `pages/files/*.tsx` | — | — | File browser, editor, tree. |
| 31 | **Web UI: Settings** | `pages/settings/*.tsx` | — | — | Provider management, security, clustering. |
| 32 | **Web UI: Terminal** | `pages/terminal/*.tsx` | — | — | Multi-session web terminal. |
| 33 | **Web UI: Logs** | `pages/logs/LogsPage.tsx` | — | — | Structured log viewer. |
| 34 | **Web UI: Core** | `App.tsx`, `ActivityBar.tsx`, `WorkerStatus.tsx` | — | — | Layout, routing, navigation. |
| 35 | **Web UI: Hooks** | `hooks/*`, `lib/*` | — | 5 test files | WebSocket, file watch, log stream, chat parsing, state. |
| 36 | **E2E Tests** | `tests/e2e/*.go` | — | 9 files | Full-stack integration tests in Docker. |
| 37 | **Cluster E2E** | `tests/e2e_cluster/` | — | 3 files | Multi-node cluster tests. |
| 38 | **Sharing** | `api/share.go` | 235 | `share_test.go` | Conversation export/import. Encrypt/decrypt roundtrip, create, access. |
| 39 | **Setup/Onboarding** | `api/setup.go`, `components/Setup.tsx` | 354+ | `setup_test.go` | First-run flow. Validation, CSRF, provider testing, swarm validation. |
| 40 | **Settings API** | `api/settings.go`, `cluster_settings.go` | 336 | `settings_test.go`, `cluster_settings_test.go` | Provider CRUD, cluster config. |
| 41 | **Cluster Nodes API** | `api/cluster_nodes.go` | 163 | `cluster_nodes_test.go` | Node approval, pending/approved/revoked management. |
| 42 | **Logs API** | `api/logs.go` | 152 | — | Log querying and SSE streaming. |
| 43 | **Usage Tracking** | `api/usage.go`, `platform/db/usage.go` | 379 | `usage_test.go` | Token/cost aggregation. |
| 44 | **LLM Providers** | `provider/provider.go` | 174 | — | Provider construction, connection testing, available provider listing. Isolates SDK imports. |

---

## 16. Hotspots — Where Complexity Lives

Files over 500 LOC or with high cyclomatic complexity deserve the most attention:

| File | LOC | Why it's hot |
|------|-----|-------------|
| `inference/loop.go` | 887 | Core loop — streaming, error recovery, tool dispatch |
| `api/terminal_session.go` | 826 | Multi-session PTY lifecycle, resize, reconnect |
| `inference/compaction.go` | 777 | Async LLM calls + locking + tree summarization |
| `agent/tools/grep.go` | 712 | Ripgrep + Go fallback — two code paths, pagination, output modes |
| `platform/db/messages.go` | 637 | Message storage + FTS + summary hierarchy |
| `agent/manager_lifecycle.go` | 611 | Instance creation, spawning, shutdown — largest agent file |
| `cmd/hiro/main.go` | 584 | CLI parsing, bootstrap, HTTP server setup |
| `api/files.go` | 555 | File CRUD — path traversal security surface |
| `api/server.go` | 528 | Router setup, middleware stack |
| `agent/manager_session.go` | 511 | Session management, model/reasoning config, agent definition push |
| `cmd/hiro/worker_node.go` | 496 | Worker node connection lifecycle |
| `api/chat.go` | 479 | WebSocket chat handler |
| `cluster/leader_service.go` | 446 | gRPC service with stream management |
| `config/markdown.go` | 445 | Parser — correctness matters |
| `inference/context_providers.go` | 441 | Context provider system — delta tracking |
| `transport/server.go` | 444 | WebSocket lifecycle, auth, routing |
| `cluster/leader_stream.go` | 437 | Leader-side stream management |
| `inference/tools_spawn.go` | 431 | Spawn + management tools |
| `cluster/relay.go` | 413 | NAT traversal — network complexity |
| `cluster/discovery.go` | 394 | HTTP-based tracker protocol |
| `cluster/worker_stream.go` | 391 | Worker connection, tool call stream |
| `platform/loghandler/handler.go` | 356 | Structured slog handler |
| `api/setup.go` | 354 | Setup flow with provider testing |
| `cluster/node_bridge.go` | 358 | Remote→local tool bridging |
| `watcher/watcher.go` | 347 | fsnotify debouncing, recursive watching |
| `agent/manager_resolve.go` | 327 | Tool rules, effective tools, provider resolution |
| `api/auth.go` | 330 | Auth middleware, rate limiting, sessions |
| `controlplane/commands.go` | 305 | Slash command parsing |

---

## 17. Quality Findings

Synthesized from deep-dive reviews of every package. Organized by priority.

### Structural — Code Organization

| Finding | Where | Impact |
|---------|-------|--------|
| ~~**manager.go is a god object**~~ | `agent/manager*.go` | **DONE** — Split into 8 focused files (155 LOC core + 7 modules). |
| ~~**local_tools.go packs 15+ tools**~~ | `inference/tools_*.go` | **DONE** — Split into 5 files by tool category. |
| ~~**controlplane.go mixes concerns**~~ | `controlplane/*.go` | **DONE** — Split into 7 focused files (211 LOC core + 6 modules). |
| ~~**agent.go imports 9 provider SDKs**~~ | `agent/agent.go` | **DONE** — Provider construction extracted to `internal/provider`. `agent.go` is now ~10 LOC (Options struct only). |
| ~~**inference→agent/tools schema coupling**~~ | `inference/tools.go` | **DONE** — Tool schemas extracted to `tools/schema.go` (`RemoteToolInfos`). Inference calls a clean schema function instead of constructing 11 dummy tool objects inline. |
| ~~**Type assertions on concrete worker types**~~ | `agent/manager_lifecycle.go` | **DONE** — `SecretEnvSetter` interface added to `ipc`. Single interface check replaces assertions on `*grpcipc.WorkerClient` and `*cluster.RemoteWorker`. |
| ~~**NodeID/HomeNodeID duplicated**~~ | `ipc`, `cluster` | **DONE** — Canonical definitions in `ipc/host_manager.go`. Cluster re-exports from ipc. |
| ~~**API Server setter injection**~~ | `api/server.go` | **DONE** — `NewServer` takes required deps (`cp`, `pdb`, `rootDir`) in constructor. Only truly late-bound setters remain (`SetManager`, `SetStartManager`, `SetWatcher`). `hasManager()` helper. |
| ~~**main.go cluster setup inline**~~ | `cmd/hiro/main.go` | **DONE** — Extracted to `bootstrap.go`: `setupNodeIdentity`, `setupClusterServer`, `bootstrapOperator`. main.go reduced from 406 to ~340 LOC. |
| ~~**filesync.go does too much**~~ (723 LOC) | `cluster/filesync*.go` | **DONE** — Split into 5 files: core (82), filter (71), initial sync (148), incremental (261), util (207). |
| ~~**Cleanup logic duplicated**~~ | `agent/manager_worker.go` | **DONE** — 4 paths consolidated into `detachWorker` (atomic field nil + status under `inst.mu`) + `teardownInstance` (parameterized post-detach I/O). |
| ~~**Resource limits scattered**~~ | `agent/tools/limits.go` | **DONE** — 18 constants from 7 files centralized into `limits.go`, organized by category (timeouts, output sizes, result limits). |

### Correctness

| Finding | Where | Severity |
|---------|-------|----------|
| ~~**Fresh tail can overflow context**~~ | `inference/assembly.go` | **FIXED** — Tail capped at 80% of budget; shrinks from oldest end with slog warning. |
| ~~**Write not atomic**~~ | `agent/tools/write.go` | **FIXED** — Uses temp+rename via `atomicWriteFile()`. |
| ~~**Memory/todos not atomic**~~ | `config/memory.go`, `config/todos.go` | **FIXED** — Both use `atomicWrite()` (temp+rename). |
| ~~**Compaction depth unbounded**~~ | `inference/compaction.go` | **FIXED** — Added `MaxSummaryDepth` (scales 4–8 with context window). Condensation stops at limit. |
| ~~**Job ID collision**~~ | `agent/tools/background.go` | **FIXED** — Widened from 12-bit (4K) to 24-bit (16M) hex ID space. |
| ~~**HandleCommand swallows save errors**~~ | `controlplane/commands.go` | **FIXED** — Appends warning to result string when Save() fails. |
| ~~**hasContent misses JoinTokens**~~ | `controlplane/controlplane.go` | **FIXED** — Join tokens now included in hasContent(); were silently not persisted. |
| ~~**SetProvider lacks validation**~~ | `controlplane/controlplane_providers.go` | **FIXED** — Returns error for empty provider type or API key. Callers updated. |
| ~~**IsInstanceDescendant infinite loop**~~ | `platform/db/instances.go` | **FIXED** — Cycle detection via visited set + max depth (100). Handles `sql.ErrNoRows` gracefully. |
| ~~**Missing InstanceID in session queries**~~ | `platform/db/sessions.go` | **FIXED** — All queries (`GetSession`, `ListSessions`, `ListSessionsByInstance`, `LatestSessionByInstance`, `scanSessions`) now select and populate `instance_id`. |
| ~~**LatestSessionByInstance swallows errors**~~ | `platform/db/sessions.go` | **FIXED** — Returns `(Session, bool, error)` to distinguish "not found" from DB failure. |
| ~~**RowsAffected errors ignored**~~ | `platform/db/instances.go`, `sessions.go` | **FIXED** — All 5 locations now check `RowsAffected()` error. |
| ~~**Assembly error swallowed**~~ | `inference/loop.go` | **FIXED** — Logged at Error level; nil guard prevents using zero-value messages. |
| ~~**History tool errors leaked into content**~~ | `inference/tools_history.go` | **FIXED** — DB errors now return `NewTextErrorResponse` instead of embedding in output. |

### Security Surface

| Finding | Where | Severity |
|---------|-------|----------|
| **Path security is strong** | `api/files.go` | Positive — Defense-in-depth: lexical + symlink + TOCTOU checks. Well-tested. |
| **mTLS + join tokens** | `cluster/tls.go`, `discovery.go` | Positive — Constant-time token comparison, Ed25519 identity, cert pinning. |
| **Secret redaction** | `inference/redact.go` | Positive — Sorts by length desc, min 8-byte guard. |
| ~~**resolve.go doesn't EvalSymlinks**~~ | `agent/tools/resolve.go` | **FIXED** — EvalSymlinks added; rejects paths that resolve outside roots via symlink. |
| ~~**API key masking revealed short keys**~~ | `controlplane/controlplane_providers.go` | **FIXED** — Raised threshold so keys ≤10 chars are fully masked. |
| ~~**Auth secret accepted without validation**~~ | `auth/auth.go` | **FIXED** — `NewTokenSigner` returns error for secrets < 32 bytes. |
| ~~**Spawn socket dir errors ignored**~~ | `agent/spawn.go` | **FIXED** — `os.MkdirAll` and `os.Chown` errors now returned, failing fast on broken isolation. |
| ~~**rand.Read unchecked in node ID**~~ | `cluster/leader_stream.go` | **FIXED** — Error checked and propagated. |
| ~~**SSRF protection opt-in**~~ | `agent/tools/fetch.go` | **FIXED** — Defaults to true (`atomic.Bool`). Pre-dial DNS resolution prevents rebinding. |
| **Relay status bytes unauthenticated** | `cluster/relay.go` | Low — MITM could inject false status. Mitigated by mTLS on the data path. |
| ~~**Rate limiter ignores reverse proxy**~~ | `api/auth.go` | **FIXED** — `clientIP()` trusts proxy headers only from loopback/private peers. Strips port. |
| ~~**Setup CSRF vulnerable to DNS rebinding**~~ | `api/server.go` | **FIXED** — `isLoopbackOrigin()` requires loopback host when Origin header present. |
| ~~**Password change silently logs out user**~~ | `api/auth.go` | **FIXED** — New session token issued in response after secret rotation. |

### Concurrency

| Finding | Where | Notes |
|---------|-------|-------|
| **Lock hierarchy well-documented** | `agent/manager.go` | `m.mu → inst.mu` ordering prevents deadlocks. |
| **Dual compaction locks** | `inference/loop.go` | `updateMu` (fast config) + `compactMu` (slow DB) — good design. |
| ~~**TokenSigner write-locks on hot path**~~ | `controlplane/controlplane_auth.go` | **FIXED** — Read-lock fast path with double-checked write-lock upgrade. |
| ~~**Race on ephemeralMsgs**~~ | `inference/loop.go` | **FIXED** — New `ephemeralMu` mutex with copy-on-read pattern. |
| ~~**Race on lastShared skills cache**~~ | `inference/loop.go` | **FIXED** — New `skillsMu` mutex. Lock ordering: `updateMu` → `skillsMu`. |
| ~~**Config push uses unbounded context**~~ | `agent/manager_session.go` | **FIXED** — 10s `context.WithTimeout` for `CreateLanguageModel` in config push. |
| ~~**allowedRoots is a global**~~ | `agent/tools/resolve.go` | **FIXED** — `atomic.Value` for goroutine-safe reads; `isInsideRoots` takes roots as parameter. |
| ~~**No gRPC flow control**~~ | `cmd/hiro/bootstrap.go` | **FIXED** — `MaxConcurrentStreams(64)` per-connection cap. |
| ~~**Unbounded handler goroutines**~~ | `cluster/worker_stream.go` | **FIXED** — Added semaphore (64 concurrent handlers max). |
| ~~**No recv timeout on streams**~~ | `cmd/hiro/bootstrap.go` | **FIXED** — gRPC keepalive: ping every 30s, 10s timeout for hung node detection. |

### Testing Gaps

| Gap | Where | Recommendation |
|-----|-------|----------------|
| ~~**API endpoints largely untested**~~ | `api/` | **DONE** — 101 tests across 9 files covering auth, instances, settings, usage, files, setup, share, origin. Remaining gaps: chat WebSocket, terminal WebSocket. |
| ~~**Control plane providers/cluster untested**~~ | `controlplane/` | **DONE** — 25 → 53 tests. Providers, cluster commands/config, auth getters, env overrides, error paths. |
| **No CI/CD pipeline** | Project-wide | All testing is manual via Makefile. Add GitHub Actions. |
| **No integration tests for manager lifecycle** | `agent/manager_test.go` | Mock worker is simplistic. Test full create→send→stop→restore flow. |
| **No concurrency stress tests for inference** | `inference/` | Model switch during Chat(), concurrent SendMessage. |
| **Cluster tests require live services** | `tests/e2e_cluster/` | Depends on `discover.hellohiro.ai` and relay. Add mock tracker option. |
| **Frontend has minimal tests** | `web/ui/` | 5 unit test files for lib utilities (`chat-parser`, `file-utils`, `format`, `session-utils`, `utils`). No component tests. |

### Web UI

| Finding | Severity | Notes |
|---------|----------|-------|
| **Strong type safety** | Positive | Strict TS, well-defined protocol types, generic architecture. |
| **Good component design** | Positive | Hooks for logic, compound components, ref-based APIs. |
| ~~**No toast/notification system**~~ | **FIXED** — Sonner toast library with theme-aware `<Toaster>` in App.tsx. |
| ~~**Error recovery shows console.error only**~~ | **FIXED** — All 6 `console.error` calls replaced with `toast.error()`. HTTP + network errors surfaced. |
| **No message virtualization** | Low | Long conversations load all messages. Could lag with 1000+ messages. |
| **No mobile responsiveness** | Low | Layout not optimized for small screens. |
| **Base64 attachments in memory** | Low | Many image attachments impact performance. |

### Database

| Finding | Notes |
|---------|-------|
| **Schema design is solid** | Proper FKs, CHECK constraints, FTS5, WAL mode, cascade deletes. |
| **FTS design is clever** | External content for integer-PK tables, standalone for text-PK. Trigger-based sync. |
| ~~**No VACUUM/optimize strategy**~~ | **FIXED** — `PRAGMA optimize` + `PRAGMA wal_checkpoint(TRUNCATE)` on `Close()`. |
| ~~**Single reader bottleneck**~~ | **FIXED** — `MaxOpenConns=4` with `_pragma` DSN params for per-connection setup. Concurrent WAL readers. |

---

## 18. Suggested Review Order

Completed items struck through. Next priorities:

1. ~~**Split `manager.go`**~~ — **DONE** (8 files, 155 LOC core).
2. ~~**Inference correctness**~~ — **DONE** (fresh tail overflow fix, compaction depth cap).
3. ~~**Split `local_tools.go`**~~ — **DONE** (5 files by tool category).
4. ~~**Cluster hardening**~~ — **DONE** (path traversal fix, node bridge robustness, goroutine bounding, gRPC flow control + keepalive recv timeouts).
5. ~~**File sync**~~ — **DONE** (Reconcile wired into production after initial sync; watch race documented as sufficient).
6. ~~**Tool correctness**~~ — **DONE** (atomic writes for Write/memory/todos, resolve.go symlink protection, job ID space widened).
7. ~~**API test coverage**~~ — **DONE** (2 → 101 tests across 9 files: auth, instances, settings, usage, files, setup, share, origin). Remaining: chat/terminal WebSocket.
8. ~~**Web UI polish**~~ — **DONE** (sonner toast system, theme-aware, all console.error→toast.error). Remaining: message virtualization, mobile responsiveness.
9. ~~**Control plane cleanup**~~ — **DONE** (split into 7 files, provider validation, save error surfacing, hasContent/JoinTokens fix, maskKey hardening, TokenSigner lock optimization, 25 → 53 tests, rate limiter proxy support, setup CSRF loopback hardening, password change session reissue).
10. ~~**Architecture refactoring**~~ — **DONE** (6 changes: extract `internal/provider`, tool schema cleanup, `SecretEnvSetter` interface, `NodeID` canonicalization, API server constructor, `bootstrap.go` extraction).
11. ~~**Error handling hardening**~~ — **DONE** (21 fixes across 18 files: infinite loop cycle detection, 2 race condition fixes with new mutexes, nil deref fix, 5 JSON marshal checks, spawn isolation errors, WalkDir errors, rand.Read check, RowsAffected checks, session query InstanceID fix, LatestSessionByInstance error surfacing, auth secret validation, config push timeout, history tool error responses, port parse check, restore config logging).
12. ~~**Structural cleanup**~~ — **DONE** (3 changes: split filesync.go 736→5 files, consolidate 4 duplicated cleanup paths into `detachWorker`+`teardownInstance`, centralize 18 resource limit constants into `limits.go`).
