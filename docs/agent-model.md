# Agent Model

This document describes Hiro's conceptual model for agents, their lifecycle, and how state is organized. This is the **target architecture** — the current codebase uses a flat session model that will be migrated to this design.

## Overview

Hiro uses a three-tier model:

```
Definition → Instance → Session
```

- A **Definition** is a template describing what an agent can be.
- An **Instance** is a durable copy of that definition with its own identity and memory.
- A **Session** is a bounded stretch of work within an instance.

Users see instances as "agents" — the thing they talk to. The definition is the blueprint they don't think about. Sessions are individual interactions within the ongoing relationship.

## Definitions

An agent definition is a markdown file with YAML frontmatter. It declares the agent's name, tools, and instructions. Definitions are templates — they carry no runtime state.

```
agents/researcher/
  agent.md          # Required. Capabilities, instructions, tool declarations.
  skills/           # Optional. Activatable skill definitions.
```

A single definition can have many instances. The definition is read fresh each time an instance or session starts, so edits take effect immediately.

## Instances

An instance is a durable copy of an agent definition. It represents the agent's **identity** — who it is, what it knows, what it has learned. This is what a user experiences as "the agent" in a chat app.

### Filesystem

Instance-level state lives on disk, organized by instance ID:

```
instances/<instance-id>/
  persona.md                    # Who this instance is (identity, tone, behavioral traits)
  memory.md                     # What the agent has learned over time
  sessions/
    <session-id>/
      todos.yaml                # Task list for this session
      scratch/                  # Working files for this session
      tmp/                      # Ephemeral files
```

Instance-level state (persona, memory) survives session boundaries. When a user clears a session, the instance — and its persona and memory — persists.

Session directories are kept on disk as long as the session exists in the database. This allows resuming a previous session with its working files intact. Directories are cleaned up when the session is deleted from the database.

### Instance Modes

An instance runs in one of three modes, specified at creation time:

| Mode | Behavior |
|------|----------|
| **Ephemeral** | Single task, then auto-deleted. Instance and session collapse into one thing. No durable state. |
| **Persistent** | Long-lived. Survives restarts. Has memory, identity, and session history. |
| **Coordinator** | Superset of persistent. Can spawn and manage child instances. Has write access to agent definitions and skills. |

### Instance Lifecycle

1. **Created** from a definition, with a mode and optional parent.
2. **Running** — has a worker process, can accept sessions.
3. **Stopped** — worker killed, state preserved on disk and in DB. Can be resumed.
4. **Deleted** — all state removed (instance directory + all DB records). Cannot be undone.

Persistent and coordinator instances survive server restarts. On boot, they are restored from the database and their workers are respawned.

## Sessions

A session is a **task-scoped** stretch of work within an instance. It groups messages, working state, and tasks for a specific interaction.

### Session Boundaries

A new session is created when:
- An instance starts for the first time
- A client connects to an instance (web UI, Telegram, API)
- A user explicitly clears the current session ("/clear")
- A parent agent spawns a task via `SendMessage`

An instance can have **multiple concurrent sessions**. Each client connection gets its own session. This prevents cross-channel interference — clearing a web session does not affect a Telegram session on the same instance.

### Session State

Session state is split between **database** and **filesystem** based on how it's accessed:

| State | Storage | Purpose |
|-------|---------|---------|
| Messages | DB | Conversation turns for this session |
| Summaries | DB | LLM-generated compaction of older messages |
| Todos | Disk (`todos.yaml`) | Task list for the current session |
| Scratch files | Disk (`scratch/`) | Working files for this session |
| Temp files | Disk (`tmp/`) | Ephemeral files, cleaned up on session end |

**DB for structured data that needs querying.** Messages and summaries live in the database because they need full-text search, cross-session queries, compaction, and token counting. The DB also enables cutting across sessions or even across instances — for example, searching all conversations a user has had with any agent.

**Filesystem for working state the agent touches directly.** Todos, scratch files, and temp files are read and written by the agent as regular files. This is consistent with how instance-level state (persona.md, memory.md) works — simple files that the agent and tools can access naturally.

### Session Lifecycle

1. **Created** — new DB record + session directory on disk.
2. **Active** — messages accumulate, context is assembled from this session's messages + instance memory.
3. **Ended** — worker processes for this session are terminated and cleaned up. Messages remain in DB for history search. Session directory remains on disk.
4. **Deleted** — DB records and session directory removed.

### Worker Processes

Each active session has its own worker process. When a session ends:
- All background jobs are terminated
- The worker process is shut down
- No processes carry over to the next session

Long-lived work that needs to survive session boundaries (scheduled jobs, monitoring, services) uses separate platform-level tooling, not session-scoped processes.

## Context Assembly

Each turn, the system prompt is assembled from all three layers:

1. **From the definition:** agent.md body, skills listing (capabilities)
2. **From the instance:** persona.md, memory.md (durable context)
3. **From the session:** todos, messages, summaries (task context)

The agent always knows who it is (persona) and what it's currently working on (session).

## History Search

Agents can search their conversation history with a scope parameter:

- **Session scope** — search only the current session's messages and summaries.
- **Instance scope** — search across all sessions for this instance.

This lets an agent answer "what were we just talking about?" (session) and "what did we discuss last time?" (instance).

## Ephemeral Agents

For ephemeral agents, the instance and session are the same thing. There is no durable state — the agent runs a single task, returns a result, and everything is cleaned up. No instance directory is created on disk. This is the common case for spawned sub-agents doing one-off work.

## Parent-Child Relationships

Instances form a tree. The coordinator is the root. When a coordinator spawns a child, the parent-child relationship is tracked at the **instance level**. Coordinator tools (SendMessage, StopInstance, etc.) are scoped to descendants — an instance cannot manage siblings or ancestors.

```
coordinator (coordinator mode)
├── researcher-1 (persistent)
│   ├── session: "analyze dataset"       (ended)
│   ├── session: "follow-up questions"   (active, web)
│   └── session: "quick check"           (active, telegram)
├── researcher-2 (persistent)
│   └── session: "literature review"     (active)
└── quick-task (ephemeral)
    └── session: (single task, auto-deleted)
```

## User-Facing vs Internal Naming

| Context | Definition | Instance | Session |
|---------|-----------|----------|---------|
| **UI** | (hidden) | "Agent" | "Session" or implicit |
| **API** | Agent definition | Agent | Session |
| **Filesystem** | `agents/<name>/` | `instances/<id>/` | `instances/<id>/sessions/<id>/` |
| **Code** | `AgentDefinition` / `AgentConfig` | `Instance` | `Session` |
| **Database** | — | `instances` table | `sessions` table |

## Summary

| Concept | What it is | State it owns | Lifetime |
|---------|-----------|---------------|----------|
| **Definition** | Template (agent.md + supporting files) | None (read-only) | Permanent |
| **Instance** | Durable identity of an agent | Memory, identity | Created → stopped/resumed → deleted |
| **Session** | A stretch of work within an instance | Messages (DB), summaries (DB), todos (disk), scratch (disk), worker | Created → ended → deleted |
