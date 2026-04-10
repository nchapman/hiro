package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// channelWeb is the default channel key for web UI and direct API calls.
const channelWeb = "web"

// SendMessage sends a message to a running instance, auto-resolving the session
// from the caller context. For management tool messages (parent→child), the
// session is keyed by the caller's instance ID. onEvent is called for each
// streaming event; it may be nil.
func (m *Manager) SendMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessageWithFiles(ctx, instanceID, message, nil, onEvent)
}

// SendMessageWithFiles is like SendMessage but includes file attachments.
func (m *Manager) SendMessageWithFiles(ctx context.Context, instanceID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	// Determine the channel key from the caller context.
	callerID := inference.CallerIDFromContext(ctx)
	channelKey := channelWeb // default for direct calls (e.g. operator bootstrap)
	if callerID != "" {
		channelKey = "agent:" + callerID
	}

	sessionID, err := m.EnsureSession(ctx, instanceID, channelKey)
	if err != nil {
		return "", err
	}
	return m.SendMessageToSessionWithFiles(ctx, instanceID, sessionID, message, files, onEvent)
}

// SendMessageToSession sends a message to a specific session on a running instance.
func (m *Manager) SendMessageToSession(ctx context.Context, instanceID, sessionID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessageToSessionWithFiles(ctx, instanceID, sessionID, message, nil, onEvent)
}

// SendMessageToSessionWithFiles sends a message with file attachments to a specific session.
func (m *Manager) SendMessageToSessionWithFiles(ctx context.Context, instanceID, sessionID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	slot, ctx, err := m.acquireSlot(ctx, instanceID, sessionID)
	if err != nil {
		return "", err
	}
	defer slot.mu.Unlock()

	slot.lastUsed = time.Now()
	return slot.loop.Chat(ctx, message, files, onEvent)
}

// SendMetaMessage runs an inference turn triggered by a notification (not a
// user message). The prompt is stored as a meta message — visible to the model
// but hidden from the user's transcript.
func (m *Manager) SendMetaMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	// Meta messages use the caller context to determine the session.
	callerID := inference.CallerIDFromContext(ctx)
	channelKey := channelWeb
	if callerID != "" {
		channelKey = "agent:" + callerID
	}
	sessionID, err := m.EnsureSession(ctx, instanceID, channelKey)
	if err != nil {
		return "", err
	}
	return m.SendMetaMessageToSession(ctx, instanceID, sessionID, message, onEvent)
}

// SendMetaMessageToSession runs a meta inference turn on a specific session.
func (m *Manager) SendMetaMessageToSession(ctx context.Context, instanceID, sessionID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	slot, ctx, err := m.acquireSlot(ctx, instanceID, sessionID)
	if err != nil {
		return "", err
	}
	defer slot.mu.Unlock()

	slot.lastUsed = time.Now()
	return slot.loop.ChatMeta(ctx, message, onEvent)
}

// acquireSlot performs cycle detection, finds the instance and session slot,
// acquires the slot lock, and validates it has a live loop. Returns the locked
// slot and an updated context with call chain info. The caller must unlock slot.mu.
func (m *Manager) acquireSlot(ctx context.Context, instanceID, sessionID string) (*sessionSlot, context.Context, error) {
	if inference.IsInCallChain(ctx, instanceID) {
		return nil, ctx, fmt.Errorf("circular message dependency: instance %s is already awaiting a response in this call chain", instanceID)
	}

	inst := m.getInstance(instanceID)
	if inst == nil {
		return nil, ctx, fmt.Errorf("%w: %s", ErrInstanceNotFound, instanceID)
	}

	inst.mu.Lock()
	if inst.info.Status == InstanceStatusStopped {
		inst.mu.Unlock()
		return nil, ctx, fmt.Errorf("instance %q: %w", instanceID, ErrInstanceStopped)
	}
	slot := inst.sessions[sessionID]
	inst.mu.Unlock()

	if slot == nil {
		return nil, ctx, fmt.Errorf("session %q not found on instance %q", sessionID, instanceID)
	}

	slot.mu.Lock()
	if slot.loop == nil {
		slot.mu.Unlock()
		return nil, ctx, fmt.Errorf("session %q has no inference loop (idle-stopped?)", sessionID)
	}

	ctx = inference.ContextWithCallChain(ctx, instanceID)
	ctx = inference.ContextWithCallerID(ctx, instanceID)

	return slot, ctx, nil
}

// InstanceNotifications returns the notification queue for an instance.
// Returns nil if the instance is not found.
func (m *Manager) InstanceNotifications(instanceID string) *inference.NotificationQueue {
	inst := m.getInstance(instanceID)
	if inst == nil {
		return nil
	}
	return inst.notifications
}

// SecretNames returns the names of available secrets from the control plane.
func (m *Manager) SecretNames() []string {
	if m.cp == nil {
		return nil
	}
	return m.cp.SecretNames()
}

// SecretEnv returns secret env vars from the control plane.
func (m *Manager) SecretEnv() []string {
	if m.cp == nil {
		return nil
	}
	return m.cp.SecretEnv()
}

// SetLifecycleHook registers a hook that is called when instances start or stop.
// Must be set before RestoreInstances so restored instances trigger the hook.
func (m *Manager) SetLifecycleHook(hook InstanceLifecycleHook) {
	m.lifecycleHook = hook
}

// SetConfigLocker sets the instance config locker for serializing
// read-modify-write operations across all config writers.
func (m *Manager) SetConfigLocker(locker config.InstanceConfigLocker) {
	m.configLocker = locker
}

// RestartChannels tears down and re-creates channels for a running instance
// by cycling the lifecycle hook. This is used when channel config changes
// via the API. Uses the manager's long-lived context so channels survive
// beyond the HTTP request that triggered the restart.
func (m *Manager) RestartChannels(instanceID string) {
	if m.lifecycleHook == nil {
		return
	}
	m.lifecycleHook.OnInstanceStop(instanceID)
	if err := m.lifecycleHook.OnInstanceStart(m.ctx, instanceID, m.instanceConfigPath(instanceID)); err != nil {
		m.logger.Warn("lifecycle hook restart failed", "instance", instanceID, "error", err)
	}
}

// SetScheduler sets the cron scheduler for subscription management.
func (m *Manager) SetScheduler(s *Scheduler) {
	m.scheduler = s
}

// GetScheduler returns the cron scheduler, or nil if not set.
func (m *Manager) GetScheduler() *Scheduler {
	return m.scheduler
}

// SetTimezone sets the server timezone for cron evaluation.
func (m *Manager) SetTimezone(tz *time.Location) {
	m.timezone = tz
}

// SetClusterService sets the cluster leader service for remote node management.
// Must be called before any remote spawns. If nil, all spawns are local.
func (m *Manager) SetClusterService(svc *cluster.LeaderService) {
	m.clusterService = svc

	// Wire up background job completion notifications from remote workers.
	svc.SetJobCompletionHandler(func(sessionID string, completion *pb.JobCompletionNotify) {
		inst := m.instanceBySession(sessionID)
		if inst == nil {
			return
		}
		inst.notifications.Push(formatJobNotification(&pb.JobCompletion{
			TaskId:      completion.TaskId,
			Command:     completion.Command,
			Description: completion.Description,
			ExitCode:    completion.ExitCode,
			Failed:      completion.Failed,
		}))
	})
}

// ListNodes returns all nodes in the cluster registry. Returns nil if
// clustering is not enabled.
func (m *Manager) ListNodes() []ipc.NodeInfo {
	if m.clusterService == nil {
		return nil
	}
	nodes := m.clusterService.Registry().List()
	result := make([]ipc.NodeInfo, len(nodes))
	for i, n := range nodes {
		result[i] = ipc.NodeInfo{
			ID:          n.ID,
			Name:        n.Name,
			Status:      string(n.Status),
			IsHome:      n.IsHome,
			Capacity:    n.Capacity,
			ActiveCount: n.ActiveCount,
		}
	}
	return result
}

// extractAgentName extracts the agent name from a watcher path like "agents/foo/agent.md".
func extractAgentName(path string) string {
	const maxPathParts = 3
	parts := strings.SplitN(path, "/", maxPathParts)
	if len(parts) < 2 || parts[0] != "agents" {
		return ""
	}
	return parts[1]
}

// Path helpers

func (m *Manager) agentDefDir(name string) string {
	return filepath.Join(m.rootDir, "agents", name)
}

func (m *Manager) sharedSkillsDir() string {
	return filepath.Join(m.rootDir, "skills")
}

func (m *Manager) instanceDir(id string) string {
	return filepath.Join(m.rootDir, "instances", id)
}

// InstanceDir returns the filesystem path for an instance's directory.
func (m *Manager) InstanceDir(id string) string {
	return m.instanceDir(id)
}

func (m *Manager) instanceConfigPath(id string) string {
	return config.InstanceConfigPath(m.rootDir, id)
}

// InstanceConfigPath returns the path to an instance's config file.
// Config files live at config/instances/<id>.yaml, outside the instance
// directory, so Landlock prevents agents from modifying their own config.
func (m *Manager) InstanceConfigPath(id string) string {
	return m.instanceConfigPath(id)
}

func (m *Manager) instanceSessionDir(instanceID, sessionID string) string {
	return filepath.Join(m.rootDir, "instances", instanceID, "sessions", sessionID)
}

// setInstanceStatus updates the instance status in the platform database.
func (m *Manager) setInstanceStatus(id, status string) {
	if m.pdb == nil {
		return
	}
	if err := m.pdb.UpdateInstanceStatus(context.Background(), id, status); err != nil {
		m.logger.Warn("failed to update instance status in db", "id", id, "status", status, "error", err)
	}
}

// instanceBySession finds the instance that owns the given session ID.
// Returns nil if no match is found.
func (m *Manager) instanceBySession(sessionID string) *instance {
	m.mu.RLock()
	snapshot := make([]*instance, 0, len(m.instances))
	for _, inst := range m.instances {
		snapshot = append(snapshot, inst)
	}
	m.mu.RUnlock()

	for _, inst := range snapshot {
		inst.mu.Lock()
		_, ok := inst.sessions[sessionID]
		inst.mu.Unlock()
		if ok {
			return inst
		}
	}
	return nil
}

// splitChannelKey splits a compound channel key (e.g. "tg:12345") into
// channel type and channel ID. Keys without a colon return (key, "").
func splitChannelKey(key string) (channelType, channelID string) {
	if ct, ci, ok := strings.Cut(key, ":"); ok {
		return ct, ci
	}
	return key, ""
}

// makeChannelKey builds a compound channel key from type and ID.
func makeChannelKey(channelType, channelID string) string {
	if channelID == "" {
		return channelType
	}
	return channelType + ":" + channelID
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateAgentName rejects names that could escape the agents directory.
func validateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: only letters, numbers, hyphens, and underscores are allowed", name)
	}
	return nil
}
