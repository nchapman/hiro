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

// MaxSessionPrefix is the maximum length of a session ID prefix used in Unix
// socket paths. Keeps paths under the 104-byte OS limit.
const MaxSessionPrefix = 18

// LandlockPaths specifies filesystem access rules for Landlock.
type LandlockPaths struct {
	ReadWrite []string `json:"rw,omitempty"`
	ReadOnly  []string `json:"ro,omitempty"`
}

// SpawnConfig is the configuration passed to an agent worker process at startup.
// Workers are thin tool-execution sandboxes — they only need paths and isolation config.
type SpawnConfig struct {
	InstanceID     string          `json:"instance_id"`
	SessionID      string          `json:"session_id"`
	AgentName      string          `json:"agent_name"`
	EffectiveTools map[string]bool `json:"effective_tools"`
	WorkingDir     string          `json:"working_dir"`
	SessionDir     string          `json:"session_dir"`
	AgentSocket    string          `json:"agent_socket"`
	LandlockPaths  LandlockPaths   `json:"landlock_paths"`
	// ReadableRoots is the list of directories that Read, Glob, and Grep may
	// address. Mirrors the policy's RW + RO paths under the platform root.
	// System paths like /usr are excluded so exec'd commands can still find
	// their libraries, but agents cannot browse them through the file tools.
	ReadableRoots []string `json:"readable_roots,omitempty"`
	// WritableRoots is the list of directories that Write and Edit may
	// address — the policy's RW paths only. An RO-in-policy path (like
	// agents/ for a non-operator agent) passes Read confinement but fails
	// Write confinement. When Landlock is unavailable, this split is the
	// only filesystem confinement the worker has.
	WritableRoots []string `json:"writable_roots,omitempty"`
	NetworkAccess bool     `json:"network_access"` // true if agent has Bash tool (sockets allowed)
}
