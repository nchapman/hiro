# Scheduling

This document describes how agents schedule recurring and one-time tasks. Scheduling is the first trigger type in Hiro's event system — the same subscription infrastructure will support webhooks, file watches, and other triggers in the future.

## Overview

Any persistent agent can schedule tasks that fire on a cron expression or at a specific time. When a schedule fires, the agent receives a message in an isolated **triggered session** — separate from the user's conversation. The agent does its work (tool calls, reasoning, etc.) and optionally surfaces results to the user via the `Notify` tool.

```
Agent calls ScheduleRecurring("0 9 * * *", "Generate daily report")
  → Subscription created in DB + scheduler heap

At 9:00am:
  Scheduler fires
    → Triggered session created/resumed for this subscription
    → Agent runs inference turn with full tool access
    → Agent calls Notify("Here's today's report: ...")
    → Notification pushed to user's primary session
    → User sees it in chat (WebSocket, Telegram, etc.)
```

The mental model: **triggered sessions are the agent's notes, Notify is the text it sends you.** The user sees a clean result; the messy work (tool calls, retries, intermediate reasoning) stays in the triggered session. Users can inspect triggered sessions for details, but the primary interaction is the notification.

## Tools

All schedule tools are gated on persistent mode and must be declared in `allowed_tools`.

| Tool | Purpose |
|------|---------|
| `ScheduleRecurring` | Create a cron-based recurring schedule |
| `ScheduleOnce` | Create a one-time schedule (relative or absolute time) |
| `CancelSchedule` | Remove a schedule by name |
| `ListSchedules` | List all schedules for this instance |
| `Notify` | Push a message to the user's primary session (triggered sessions only) |

### ScheduleRecurring

```
ScheduleRecurring(name, schedule, message)
  name:     "daily-report"         — unique per instance
  schedule: "0 9 * * *"            — 5-field cron expression
  message:  "Generate the report"  — prompt delivered on each fire
```

Standard 5-field cron (minute, hour, day-of-month, month, day-of-week). All times use the server's configured timezone (`config.yaml` → `timezone`, defaults to UTC).

### ScheduleOnce

```
ScheduleOnce(name, at, message)
  name:    "reminder"
  at:      "30m"                               — relative duration
        or "2026-04-05T17:00:00"               — absolute (server tz)
        or "2026-04-05T17:00:00Z"              — absolute (UTC)
  message: "Check on the deployment"
```

Relative durations (Go `time.Duration` format: `20m`, `2h`, `1h30m`) are resolved to an absolute time at creation. The subscription is automatically deleted after a successful fire.

### Notify

Available only in triggered sessions. Pushes a message to the instance's notification queue with empty `SessionID`, so it's delivered to whatever primary session is active. The `chatEventLoop` (or equivalent client handler) picks it up via `SendMetaMessage`.

Agents should be selective — a health check that finds no issues shouldn't notify. A health check that finds a problem should notify immediately.

## Data Model

### subscriptions table

```sql
CREATE TABLE subscriptions (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    trigger     TEXT NOT NULL,    -- JSON: {"type":"cron","expr":"0 9 * * *"}
                                 --    or {"type":"once","at":"2026-04-05T17:00:00Z"}
    message     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',  -- active | paused
    next_fire   TEXT,            -- precomputed UTC datetime
    last_fired  TEXT,
    fire_count  INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(instance_id, name)
);
```

The `trigger` column is a JSON blob with a `type` discriminator. Currently supports `cron` and `once`. Adding new trigger types requires no schema migration — just new JSON shapes and scheduler handling.

`next_fire` is precomputed at creation and after each fire. The scheduler's hot query is:
```sql
SELECT * FROM subscriptions WHERE status = 'active' AND next_fire IS NOT NULL ORDER BY next_fire ASC
```

### Triggered sessions

Triggered sessions are regular sessions linked to a subscription via `sessions.subscription_id`. Each subscription gets one session that persists across fires — the agent can reference yesterday's report when writing today's. Sessions are preserved even after a subscription is cancelled (`ON DELETE SET NULL`).

## Scheduler

The scheduler runs in the control plane as a single goroutine with a min-heap priority queue ordered by `next_fire`.

### Architecture

```
Scheduler (single goroutine)
  ├── Min-heap of subEntries (sorted by nextFire)
  ├── Wake channel (signaled on Add/Remove/fire completion)
  ├── Per-subscription overlap guard (running map)
  ├── Cancelled set (prevents re-add after Remove/Pause during fire)
  └── WaitGroup (tracks in-flight fire goroutines)

Fire flow:
  run loop sleeps until next fire time
    → fireReady pops due entries
    → each fires in its own goroutine (wg.Go)
    → fireSingle: RunTriggered → update DB → re-add to heap
    → signal run loop to re-evaluate
```

### Key design decisions

**Priority queue, not polling.** The scheduler sleeps until the next fire time rather than polling all subscriptions every N seconds. O(log n) per fire, fires at the exact second, zero CPU with zero subscriptions.

**Goroutines per fire, not sequential.** Each subscription fires in its own goroutine so a slow inference turn doesn't block other subscriptions. A per-subscription `running` guard prevents overlapping fires (if a cron fires every minute but the turn takes 3 minutes, the overlapping fires are skipped).

**Cancelled set for mid-flight removal.** When `Remove` or `PauseInstance` is called while a subscription is actively firing (entry popped from heap, goroutine running), the ID is added to a `cancelled` set. When `fireSingle` completes, it checks this set before re-adding to the heap.

**Cached cron parsing.** The `cron.Schedule` is parsed once at insertion time and cached in the heap entry. No re-parsing on every fire.

**10-minute timeout per triggered turn.** `RunTriggered` applies `context.WithTimeout` to prevent a hung LLM call from holding `inst.mu` indefinitely.

### Lifecycle integration

| Event | Action |
|-------|--------|
| Instance stopped (`softStop`) | `PauseInstance` — subscriptions set to `paused` in DB, removed from heap |
| Instance started (`StartInstance`) | `ResumeInstance` — subscriptions set to `active`, `next_fire` recomputed, added to heap |
| Instance deleted (`DeleteInstance`) | `PauseInstance` (clears heap), then `ON DELETE CASCADE` removes DB rows |
| Server startup | `Start()` loads all active subscriptions from DB, builds heap |
| Server shutdown | `Stop()` cancels context, waits for run loop + all fire goroutines |

### Concurrency

Lock ordering: `m.mu → inst.mu → s.mu`. The scheduler's `s.mu` is a leaf lock — nothing acquires `m.mu` or `inst.mu` while holding `s.mu`.

`RunTriggered` holds `inst.mu` for the full inference turn, serializing with `SendMessage` and `SendMetaMessage`. This is required because the worker process cannot safely handle concurrent tool execution (shared env vars, CWD, and background task registry). A triggered turn blocks the user's primary session for its duration.

## Context Provider

`SubscriptionProvider` shows active schedules in `<system-reminder>` messages using the content-hash delta strategy. The agent sees its schedules every turn without calling `ListSchedules`:

```
## Active Schedules

| Name | Type | Schedule | Status | Next Fire |
|------|------|----------|--------|-----------|
| daily-report | cron | `0 9 * * *` | active | 2026-04-06 09:00 |
| reminder | once | `2026-04-05T17:00:00Z` | active | 2026-04-05 17:00 |
```

Gated on `ScheduleRecurring` being in the instance's active tools.

## Server Timezone

A single server-level timezone in `config.yaml`:

```yaml
timezone: America/New_York
```

Defaults to UTC. Used for all cron evaluation — agents just write `"0 9 * * *"` and it means 9am in the server's timezone. No per-subscription timezone complexity.

## Key Files

| File | Purpose |
|------|---------|
| `internal/platform/db/migrations/009_subscriptions.sql` | Schema |
| `internal/platform/db/subscriptions.go` | Subscription CRUD |
| `internal/agent/scheduler.go` | Scheduler, RunTriggered, ensureTriggeredSession, heap |
| `internal/inference/tools_schedule.go` | ScheduleRecurring, ScheduleOnce, CancelSchedule, ListSchedules |
| `internal/inference/tools_notify.go` | Notify tool |
| `internal/inference/context_subscriptions.go` | Context provider |
| `internal/controlplane/controlplane.go` | Timezone config |

## Future Work

The subscription table and scheduler are designed to support additional trigger types:

- **Webhooks**: `{"type":"webhook","path":"/deploy"}` — HTTP handler matches path, pushes notification
- **File watches**: `{"type":"file","glob":"workspace/*.csv"}` — fsnotify triggers on match
- **Child completion**: Already handled by existing notification system (`SpawnInstance` with `background: true`)

Each new type needs: a JSON trigger shape, a `buildEntry` case in the scheduler, and a tool for agents to create subscriptions. The fire path (`RunTriggered` → triggered session → optional `Notify`) is shared.
