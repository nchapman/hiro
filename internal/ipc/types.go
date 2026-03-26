// Package ipc defines the interfaces and types for communication between
// the control plane and agent worker processes.
package ipc

// SessionInfo describes a session (agent instance) for external consumers.
type SessionInfo struct {
	ID          string
	Name        string
	Mode        string
	Description string
	ParentID    string
	Status      string // "running" or "stopped"
	Model       string // resolved model ID (e.g. "claude-sonnet-4-20250514")
}

// ConfigUpdate carries resolved structural config pushed from the control plane
// to a running inference loop when agent definitions or control plane config change.
type ConfigUpdate struct {
	EffectiveTools map[string]bool // nil = unrestricted, non-nil = allowed set
	Model          string          // resolved model (frontmatter → CP default)
	Provider       string          // resolved provider type
	APIKey         string          // resolved provider API key
	Description    string          // current description from agent.md frontmatter
}

// SpawnConfig is the configuration passed to an agent worker process at startup.
// Workers are thin tool-execution sandboxes — they only need paths and UID info.
type SpawnConfig struct {
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
