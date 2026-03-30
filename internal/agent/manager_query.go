package agent

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nchapman/hivebot/internal/inference"
	"github.com/nchapman/hivebot/internal/ipc"
)

// HistoryMessage is a simplified message for API consumers.
type HistoryMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	RawJSON   string `json:"raw_json,omitempty"` // full fantasy.Message JSON with tool calls
	Timestamp string `json:"timestamp"`
}

// ErrInstanceNotFound is returned when an instance ID does not match a known instance.
var ErrInstanceNotFound = errors.New("instance not found")

// ErrInstanceNotStopped is returned when an operation requires a stopped instance.
var ErrInstanceNotStopped = errors.New("instance is not stopped")

// GetInstance returns info about an instance (running or stopped).
func (m *Manager) GetInstance(instanceID string) (InstanceInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[instanceID]
	if !ok {
		return InstanceInfo{}, false
	}
	return inst.info, true
}

// ListInstances returns a snapshot of all instances (running and stopped) sorted by creation order.
// Instance IDs are UUIDv7 (time-ordered), so lexicographic sort gives creation order.
func (m *Manager) ListInstances() []InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]InstanceInfo, 0, len(m.instances))
	for _, inst := range m.instances {
		result = append(result, inst.info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// ListChildInstances returns a snapshot of instances that are direct children of callerInstanceID.
func (m *Manager) ListChildInstances(callerInstanceID string) []ipc.InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	childIDs := m.children[callerInstanceID]
	result := make([]ipc.InstanceInfo, 0, len(childIDs))
	for _, cid := range childIDs {
		if inst, ok := m.instances[cid]; ok {
			result = append(result, ipc.InstanceInfo{
				ID:          inst.info.ID,
				Name:        inst.info.Name,
				Mode:        string(inst.info.Mode),
				Description: inst.info.Description,
				ParentID:    inst.info.ParentID,
				Status:      string(inst.info.Status),
				Model:       inst.info.Model,
			})
		}
	}
	return result
}

// GetHistory returns recent messages from the active session of a persistent instance.
func (m *Manager) GetHistory(instanceID string, limit int) ([]HistoryMessage, error) {
	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	var sessionID string
	var persistent bool
	if ok {
		sessionID = inst.activeSession
		persistent = inst.info.Mode.IsPersistent()
	}
	m.mu.RUnlock()
	if !ok {
		return nil, ErrInstanceNotFound
	}

	if !persistent {
		return nil, nil
	}

	if m.pdb == nil {
		return nil, nil
	}
	if sessionID == "" {
		return nil, nil
	}

	msgs, err := m.pdb.RecentMessages(sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}

	result := make([]HistoryMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Role == "user" || msg.Role == "assistant" || msg.Role == "tool" {
			rawJSON := msg.RawJSON
			if msg.Role == "assistant" && rawJSON != "" {
				rawJSON = inference.InjectStatusMessages(rawJSON)
			}
			result = append(result, HistoryMessage{
				Role:      msg.Role,
				Content:   msg.Content,
				RawJSON:   rawJSON,
				Timestamp: msg.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return result, nil
}

// GetSessionHistory returns recent messages from a specific session (for history browsing).
func (m *Manager) GetSessionHistory(sessionID string, limit int) ([]HistoryMessage, error) {
	if m.pdb == nil {
		return nil, nil
	}

	msgs, err := m.pdb.RecentMessages(sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}

	result := make([]HistoryMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Role == "user" || msg.Role == "assistant" || msg.Role == "tool" {
			rawJSON := msg.RawJSON
			if msg.Role == "assistant" && rawJSON != "" {
				rawJSON = inference.InjectStatusMessages(rawJSON)
			}
			result = append(result, HistoryMessage{
				Role:      msg.Role,
				Content:   msg.Content,
				RawJSON:   rawJSON,
				Timestamp: msg.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return result, nil
}

// InstanceByAgentName returns the ID and status of an instance by agent name.
// Prefers running instances; falls back to stopped ones. If multiple
// instances share a name, running ones take priority.
func (m *Manager) InstanceByAgentName(name string) (id string, running bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for iid, inst := range m.instances {
		if inst.info.Name == name {
			if inst.info.Status == InstanceStatusRunning {
				return iid, true
			}
			// Remember the stopped one, but keep looking for a running one.
			id = iid
		}
	}
	return id, false
}

// ActiveSessionID returns the active session ID for an instance.
func (m *Manager) ActiveSessionID(instanceID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[instanceID]; ok {
		return inst.activeSession
	}
	return ""
}

// IsDescendant reports whether targetID is a descendant of (or equal to) ancestorID.
func (m *Manager) IsDescendant(targetID, ancestorID string) bool {
	descendants := m.collectDescendants(ancestorID)
	for _, id := range descendants {
		if id == targetID {
			return true
		}
	}
	return false
}

// collectDescendants returns instanceID plus all its descendants via BFS,
// in parent-first order (reverse for leaf-first shutdown).
func (m *Manager) collectDescendants(instanceID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.instances[instanceID]; !ok {
		return nil
	}

	var result []string
	queue := []string{instanceID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)
		queue = append(queue, m.children[current]...)
	}
	return result
}
