// Package ipc defines the interfaces and types for communication between
// the control plane and agent worker processes.
package ipc

// AgentInfo describes a running agent for external consumers.
type AgentInfo struct {
	ID          string
	Name        string
	Mode        string
	Description string
	ParentID    string
}

// SpawnConfig is the configuration passed to an agent worker process at startup.
type SpawnConfig struct {
	SessionID      string          `json:"session_id"`
	AgentName      string          `json:"agent_name"`
	ParentID       string          `json:"parent_id"`
	Mode           string          `json:"mode"`
	EffectiveTools map[string]bool `json:"effective_tools"`
	WorkingDir     string          `json:"working_dir"`
	SessionDir     string          `json:"session_dir"`
	AgentDefDir    string          `json:"agent_def_dir"`
	SharedSkillDir string          `json:"shared_skill_dir"`
	AgentSocket    string          `json:"agent_socket"`
	HostSocket     string          `json:"host_socket"`
	Provider       string          `json:"provider"`
	APIKey         string          `json:"api_key"`
	Model          string          `json:"model"`
	UID            uint32          `json:"uid,omitempty"` // 0 = no isolation
	GID            uint32          `json:"gid,omitempty"`
}
