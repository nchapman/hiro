package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc/grpcipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// shutdownGrace is the deadline for a graceful worker shutdown before force-killing.
const shutdownGrace = 5 * time.Second

// shutdownHandle sends a graceful shutdown to a worker handle, waits for exit
// under a deadline, and force-kills if necessary.
func (m *Manager) shutdownHandle(h *WorkerHandle) {
	if h == nil || h.Worker == nil {
		return
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := h.Worker.Shutdown(shutCtx); err != nil {
		m.logger.Debug("worker shutdown error", "error", err)
	}

	select {
	case <-h.Done:
		// Process exited cleanly.
	case <-shutCtx.Done():
		// Deadline expired — force-kill and wait.
		if h.Kill != nil {
			h.Kill()
		}
		<-h.Done
	}
}

// detachAllSlots captures all session slot handles under inst.mu and nils out
// the worker/handle/loop fields on each slot. If status is non-empty, it is
// set atomically with the field nils. Returns the captured handles for
// shutdown outside the lock.
func (m *Manager) detachAllSlots(inst *instance, status InstanceStatus) []*WorkerHandle {
	inst.mu.Lock()
	var handles []*WorkerHandle
	for _, slot := range inst.sessions {
		if slot.handle != nil {
			handles = append(handles, slot.handle)
		}
		slot.worker = nil
		slot.handle = nil
		slot.loop = nil
	}
	if status != "" {
		inst.info.Status = status
	}
	inst.mu.Unlock()
	return handles
}

// softStop gracefully shuts down all session workers for a persistent instance
// but keeps it in the registry with status "stopped".
func (m *Manager) softStop(id string) {
	// Notify lifecycle hook before teardown (e.g. stop channels).
	if m.lifecycleHook != nil {
		m.lifecycleHook.OnInstanceStop(id)
	}

	// Capture all handles under both locks and mark stopped atomically.
	// Lock order: m.mu → inst.mu (no reverse path exists in the codebase).
	m.mu.Lock()
	inst, ok := m.instances[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	handles := m.detachAllSlots(inst, InstanceStatusStopped)
	m.mu.Unlock()

	// Shutdown all captured handles outside the lock (blocks on I/O).
	for _, h := range handles {
		m.shutdownHandle(h)
		h.Close()
	}
	// Release UID and update DB status.
	if m.uidPool != nil {
		m.uidPool.Release(id)
	}
	m.setInstanceStatus(id, string(InstanceStatusStopped))

	// Pause cron subscriptions for this instance.
	if m.scheduler != nil {
		m.scheduler.PauseInstance(context.Background(), id)
	}
}

// reregisterStopped puts an instance back into the registry as stopped.
// Used when StartInstance fails after unregistering.
func (m *Manager) reregisterStopped(id string, inst *instance) {
	inst.mu.Lock()
	inst.info.Status = InstanceStatusStopped
	inst.mu.Unlock()

	m.mu.Lock()
	m.instances[id] = inst
	if inst.info.ParentID != "" {
		m.children[inst.info.ParentID] = append(m.children[inst.info.ParentID], id)
	}
	m.mu.Unlock()
}

// watchSlotWorker monitors a single session slot's worker Done channel and
// handles unexpected exits. The handle parameter identifies which worker
// generation this goroutine is watching — if NewSessionForChannel has replaced
// it by the time we acquire the lock, this exit is stale and should be ignored.
func (m *Manager) watchSlotWorker(instanceID, sessionID string, handle *WorkerHandle) {
	<-handle.Done

	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	var name string
	if ok {
		name = inst.agentName
	}
	m.mu.RUnlock()
	if !ok {
		return
	}

	// Check under inst.mu whether this exit was intentional.
	inst.mu.Lock()
	slot := inst.sessions[sessionID]
	stale := inst.info.Status == InstanceStatusStopped || slot == nil || slot.handle != handle
	if !stale && slot != nil {
		// Unexpected exit — remove this slot.
		inst.removeSlot(sessionID)
	}
	allDead := len(inst.sessions) == 0
	inst.mu.Unlock()

	if stale {
		return
	}

	m.logger.Warn("session worker exited unexpectedly",
		"instance", instanceID,
		"session", sessionID,
		"name", name,
	)

	// Clean up the dead worker's resources.
	handle.Close()

	// If all slots are dead on this instance, handle the full instance death
	// (same as the old watchWorker behavior for single-session).
	if !allDead {
		return
	}

	m.logger.Warn("all session workers dead, handling instance death",
		"id", instanceID,
		"name", name,
	)
	m.handleInstanceDeath(instanceID)
}

// handleInstanceDeath handles cascading teardown when all session workers for an
// instance have died unexpectedly.
func (m *Manager) handleInstanceDeath(instanceID string) {
	descendants := m.collectDescendants(instanceID)
	for i := len(descendants) - 1; i >= 0; i-- {
		id := descendants[i]
		m.mu.RLock()
		deadInst, exists := m.instances[id]
		m.mu.RUnlock()
		if !exists || deadInst.info.Status == InstanceStatusStopped {
			continue
		}

		if m.lifecycleHook != nil {
			m.lifecycleHook.OnInstanceStop(id)
		}

		if deadInst.info.Mode.IsPersistent() {
			m.mu.Lock()
			if deadInst.info.Status == InstanceStatusStopped {
				m.mu.Unlock()
				continue
			}
			handles := m.detachAllSlots(deadInst, InstanceStatusStopped)
			m.mu.Unlock()

			for _, h := range handles {
				h.Close()
			}
			m.setInstanceStatus(id, string(InstanceStatusStopped))
			if m.uidPool != nil {
				m.uidPool.Release(id)
			}
		} else {
			m.mu.Lock()
			handles := m.detachAllSlots(deadInst, "")
			m.unregisterLocked(id, deadInst)
			m.mu.Unlock()

			for _, h := range handles {
				h.Close()
			}
			os.RemoveAll(m.instanceDir(id))
			if m.uidPool != nil {
				m.uidPool.Release(id)
			}
		}
	}
}

// watchSlotJobCompletions monitors a worker for background task completions and
// pushes notifications into the instance's notification queue with the session ID
// so they route to the correct channel. If the worker does not support WatchJobs
// (e.g. test fakes), this is a no-op.
func (m *Manager) watchSlotJobCompletions(ctx context.Context, worker any, notifications *inference.NotificationQueue, sessionID string) {
	wc, ok := worker.(*grpcipc.WorkerClient)
	if !ok {
		return
	}
	ch := wc.WatchJobs(ctx, m.logger)
	for completion := range ch {
		n := formatJobNotification(completion)
		n.SessionID = sessionID
		notifications.Push(n)
	}
}

// formatJobNotification creates a notification matching Claude Code's
// <task-notification> XML format.
func formatJobNotification(c *pb.JobCompletion) inference.Notification {
	status := "completed"
	if c.Failed {
		status = "failed"
	}
	desc := c.Description
	if desc == "" {
		desc = c.Command
	}
	var summary string
	if c.Failed {
		summary = fmt.Sprintf("Background command %q failed with exit code %d", desc, c.ExitCode)
	} else {
		summary = fmt.Sprintf("Background command %q completed (exit code %d)", desc, c.ExitCode)
	}

	content := fmt.Sprintf("<task-notification>\n<task_id>%s</task_id>\n<status>%s</status>\n<summary>%s</summary>\n</task-notification>",
		c.TaskId, status, summary)

	return inference.Notification{
		Content: content,
		Source:  "background-job",
	}
}

// removeInstance gracefully shuts down and removes an instance from the registry.
// Ephemeral instance directories are cleaned up.
func (m *Manager) removeInstance(id string) {
	// Notify lifecycle hook before teardown (e.g. stop channels).
	if m.lifecycleHook != nil {
		m.lifecycleHook.OnInstanceStop(id)
	}

	m.mu.Lock()
	inst, ok := m.instances[id]
	var handles []*WorkerHandle
	if ok {
		handles = m.detachAllSlots(inst, "")
	}
	m.unregisterLocked(id, inst)
	m.mu.Unlock()

	if !ok {
		return
	}

	// Gracefully shut down all session workers.
	for _, h := range handles {
		m.shutdownHandle(h)
		h.Close()
	}
	// Release UID.
	if m.uidPool != nil {
		m.uidPool.Release(id)
	}
	if !inst.info.Mode.IsPersistent() {
		os.RemoveAll(m.instanceDir(id))
	}
}
