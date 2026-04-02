// Package transport defines the wire protocol for communication between
// leader and worker agents in a Hiro swarm.
package transport

import "time"

// MessageType identifies the kind of message being sent over the wire.
type MessageType string

const (
	// Control messages
	TypeRegister   MessageType = "register"   // worker → leader: announce skills
	TypeRegistered MessageType = "registered" // leader → worker: confirm registration
	TypeHeartbeat  MessageType = "heartbeat"  // bidirectional keep-alive
	TypeDisconnect MessageType = "disconnect" // either direction: clean shutdown

	// Task messages
	TypeTaskRequest  MessageType = "task_request"  // leader → worker: do this task
	TypeTaskResult   MessageType = "task_result"   // worker → leader: here's the result
	TypeTaskProgress MessageType = "task_progress" // worker → leader: progress update
	TypeTaskError    MessageType = "task_error"    // worker → leader: task failed
)

// Envelope is the top-level wire format for all messages.
type Envelope struct {
	Type      MessageType `json:"type"`
	ID        string      `json:"id"`                    // unique message ID
	InReplyTo string      `json:"in_reply_to,omitempty"` // for responses
	From      string      `json:"from"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   any         `json:"payload"`
}

// RegisterPayload is sent by a worker to announce itself to the leader.
type RegisterPayload struct {
	AgentName   string   `json:"agent_name"`
	Description string   `json:"description"`
	Skills      []string `json:"skills"`
	SwarmCode   string   `json:"swarm_code"`
}

// RegisteredPayload confirms a worker's registration.
type RegisteredPayload struct {
	AgentID   string `json:"agent_id"`
	SwarmName string `json:"swarm_name"`
}

// TaskRequestPayload asks a worker to perform a task.
type TaskRequestPayload struct {
	TaskID      string `json:"task_id"`
	Skill       string `json:"skill"`
	Prompt      string `json:"prompt"`
	Context     string `json:"context,omitempty"` // additional context
	TimeoutSecs int    `json:"timeout_secs,omitempty"`
}

// TaskResultPayload contains the result of a completed task.
type TaskResultPayload struct {
	TaskID string `json:"task_id"`
	Result string `json:"result"`
}

// TaskProgressPayload provides a progress update on an in-flight task.
type TaskProgressPayload struct {
	TaskID  string `json:"task_id"`
	Message string `json:"message"`
}

// TaskErrorPayload reports a task failure.
type TaskErrorPayload struct {
	TaskID string `json:"task_id"`
	Error  string `json:"error"`
}
