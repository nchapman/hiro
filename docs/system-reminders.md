# System Reminders

This document describes how Hiro injects dynamic context into conversations using `<system-reminder>` messages with delta tracking.

## Problem

The system prompt should be **static** — it defines the agent's identity (environment, instructions, persona, security). But agents also need dynamic context: what memories they've accumulated, what tasks are in progress, which secrets and skills are available, which other agents exist.

Putting dynamic content in the system prompt busts the prompt cache every time anything changes. Injecting it as ephemeral messages that are recomputed every turn has the same problem — the LLM sees different message content each turn even when nothing changed.

## Design

Dynamic context is injected as **persisted user messages** wrapped in `<system-reminder>` tags. Each message carries structured replay metadata in Fantasy's `ProviderOptions` (invisible to the model). On each turn:

1. Providers scan conversation history to reconstruct what was previously announced
2. Compare against current state
3. If nothing changed → emit nothing → prior messages stay cached
4. If something changed → emit one new message with only the changes

Multiple providers that emit in the same turn are merged into a **single** `<system-reminder>` message. The model never sees consecutive context messages.

```
system:     Environment + Instructions + Persona + Security     ← static, always cached

messages:
  user:     <system-reminder>## Memories ... ## Skills ...</system-reminder>  ← turn 1
  user:     "fix the login bug"                                               ← turn 1
  assistant: [response + tool calls]
  user:     "now add tests"                                                   ← turn 2, no delta
  assistant: [response]
  user:     <system-reminder>## Memories (updated) ...</system-reminder>      ← turn 3, memory changed
  user:     "ship it"
```

## Change detection strategies

There are two strategies, chosen based on the shape of the data:

### Named-set delta (skills, secrets, agents)

For content that's a set of named items. Tracks additions and removals via event-sourced replay.

**Lifecycle:**
1. First turn → full announcement (all items listed)
2. Item added at runtime → delta message listing only the new item
3. Item removed → delta message noting the removal
4. After compaction → announced set is empty → full re-announcement

Each message's `ProviderOptions` stores which names were added/removed. Scanning history and replaying these operations reconstructs the current announced set without any in-memory state.

### Content hash (memory, todos)

For content that's a text blob where diffing individual changes isn't meaningful. Stores a SHA-256 hash of the content.

**Lifecycle:**
1. First turn → full content emitted, hash stored
2. Content unchanged → hash matches → no message
3. Content changed → new message with full content and new hash
4. After compaction → no prior hash found → full re-emission

## Providers

| Provider | Context type | Strategy | Gate | Data source |
|----------|-------------|----------|------|-------------|
| `MemoryProvider` | `memory` | Content hash | instanceDir set | `memory.md` |
| `TodoProvider` | `todos` | Content hash | sessionDir set | `todos.yaml` |
| `SecretProvider` | `secrets` | Named-set delta | secretNamesFn non-nil | `ControlPlane.SecretNames()` |
| `AgentListingProvider` | `agents` | Named-set delta | SpawnInstance or CreatePersistentInstance tool active | `HostManager.ListAgentDefs()` |
| `NodeListingProvider` | `nodes` | Content hash | ListNodes tool active | `HostManager.ListNodes()` |
| `SkillProvider` | `skills` | Named-set delta | Skill tool active | `config.LoadSkills()` from disk |
| `SubscriptionProvider` | `schedules` | Content hash | pdb non-nil | `pdb.ListSubscriptionsByInstance()` |

Providers are registered in order in `buildLoopConfig()` in `manager_lifecycle.go`. Registration order determines the order of sections in merged messages.

## Structured metadata

Each `<system-reminder>` message stores a `DeltaReplay` in `ProviderOptions` under the key `hiro.delta`. This metadata survives JSON round-trips through SQLite (`raw_json` column) via Fantasy's type registry but is never sent to the LLM.

```go
type DeltaReplay struct {
    Entries []DeltaEntry `json:"entries"`
}

type DeltaEntry struct {
    ContextType  string   `json:"context_type"`
    AddedNames   []string `json:"added_names,omitempty"`
    RemovedNames []string `json:"removed_names,omitempty"`
    ContentHash  string   `json:"content_hash,omitempty"`
}
```

A single message can carry entries for multiple context types (when several providers emit in the same turn).

## Self-healing after compaction

When conversation history is compacted, prior `<system-reminder>` messages are summarized away. On the next turn:

- Named-set providers replay an empty history → announced set is empty → full re-announcement
- Content-hash providers find no prior hash → re-emit current content

No special compaction handling is needed. The system is self-healing by design.

## Adding a new provider

1. Choose a strategy: named-set delta (for sets of items) or content hash (for text blobs)
2. Write a factory function in `context_providers.go` that returns a `ContextProvider` closure
3. The closure receives `activeTools map[string]bool` and `history []fantasy.Message`
4. Use `replayAnnounced()` or `replayLatestHash()` to check what was previously announced
5. Return `nil` if nothing changed; return a `*DeltaResult` with a message if something did
6. Use `buildDeltaMessage()` for named-set or `buildContentMessage()` for content-hash
7. Register the provider in `buildLoopConfig()` in `manager_lifecycle.go`

## Key files

| File | Role |
|------|------|
| `internal/inference/context_providers.go` | Core types, helpers, and most provider implementations |
| `internal/inference/tools_spawn.go` | `AgentListingProvider`, `NodeListingProvider` (co-located with spawn tools) |
| `internal/inference/context_subscriptions.go` | `SubscriptionProvider` (schedule listing for persistent agents) |
| `internal/inference/loop.go` | `applyContextDeltas()` — calls providers and persists results |
| `internal/inference/prompt.go` | System prompt (static identity only) |
| `internal/agent/manager_lifecycle.go` | Provider registration for persistent instances |
| `internal/agent/manager_session.go` | Provider registration for new sessions |
