// Package ipc defines the interfaces and types for communication between
// the control plane and agent worker processes.
package ipc

import "time"

// InstanceInfo describes an agent instance for external consumers.
// Name and Description are resolved: persona.md frontmatter overrides
// the agent definition defaults.
type InstanceInfo struct {
	ID          string
	Name        string // resolved: persona name > agent definition name
	Mode        string
	Description string // resolved: persona description > agent definition description
	ParentID    string
	Status      string // "running" or "stopped"
	Model       string // resolved model ID (e.g. "claude-sonnet-4-20250514")
}

// SessionInfo describes a session within an instance.
type SessionInfo struct {
	ID         string
	InstanceID string
	Status     string // "running", "stopped"
	CreatedAt  time.Time
}

// SpawnConfig is the configuration passed to an agent worker process at startup.
// Workers are thin tool-execution sandboxes — they only need paths and UID info.
type SpawnConfig struct {
	InstanceID     string          `json:"instance_id"`
	SessionID      string          `json:"session_id"`
	AgentName      string          `json:"agent_name"`
	EffectiveTools map[string]bool `json:"effective_tools"`
	WorkingDir     string          `json:"working_dir"`
	SessionDir     string          `json:"session_dir"`
	AgentSocket    string          `json:"agent_socket"`
	UID            uint32          `json:"uid,omitempty"` // 0 = no isolation
	GID            uint32          `json:"gid,omitempty"`
	Groups         []uint32        `json:"groups,omitempty"`
}
