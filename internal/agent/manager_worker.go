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
	h.Worker.Shutdown(shutCtx)

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

// cleanupWorker closes the worker handle and releases the UID.
// The handle is captured under the lock to avoid races.
func (m *Manager) cleanupWorker(id string, h *WorkerHandle) {
	if h != nil {
		h.Close()
	}
	if m.uidPool != nil {
		m.uidPool.Release(id)
	}
}

// detachWorker captures the worker handle under inst.mu and nils out the
// worker, handle, and loop fields. If status is non-empty, it is set
// atomically with the field nils. The returned handle can be shut down
// outside both locks without races; callers must not hold inst.mu when
// calling shutdownHandle (it blocks on I/O).
func (m *Manager) detachWorker(inst *instance, status InstanceStatus) *WorkerHandle {
	inst.mu.Lock()
	h := inst.handle
	inst.worker = nil
	inst.handle = nil
	inst.loop = nil
	if status != "" {
		inst.info.Status = status
	}
	inst.mu.Unlock()
	return h
}

// teardownOpts controls what teardownInstance does after the worker is detached.
type teardownOpts struct {
	graceful  bool           // call shutdownHandle (false when process already dead)
	removeDir bool           // os.RemoveAll(instanceDir)
	status    InstanceStatus // set instance status (e.g. InstanceStatusStopped), zero = skip
}

// teardownInstance performs post-detach cleanup: optional graceful shutdown,
// worker resource cleanup, status persistence, and directory removal.
func (m *Manager) teardownInstance(id string, h *WorkerHandle, opts teardownOpts) {
	if opts.graceful && h != nil {
		m.shutdownHandle(h)
	}
	m.cleanupWorker(id, h)
	if opts.status != "" {
		m.setInstanceStatus(id, string(opts.status))
	}
	if opts.removeDir {
		os.RemoveAll(m.instanceDir(id))
	}
}

// softStop gracefully shuts down a persistent instance's worker process
// but keeps it in the registry with status "stopped".
func (m *Manager) softStop(id string) {
	// Capture the handle under both locks and mark stopped atomically.
	// Lock order: m.mu → inst.mu (no reverse path exists in the codebase).
	// Both locks are needed: m.mu for watchWorker, inst.mu for SendMessage/UpdateConfig.
	m.mu.Lock()
	inst, ok := m.instances[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	h := m.detachWorker(inst, InstanceStatusStopped)
	m.mu.Unlock()

	// Shutdown the captured handle outside the lock (blocks on I/O).
	m.teardownInstance(id, h, teardownOpts{graceful: true, status: InstanceStatusStopped})
}

// reregisterStopped puts an instance back into the registry as stopped.
// Used when StartInstance fails after unregistering.
func (m *Manager) reregisterStopped(id string, inst *instance) {
	m.mu.Lock()
	inst.info.Status = InstanceStatusStopped
	m.instances[id] = inst
	if inst.info.ParentID != "" {
		m.children[inst.info.ParentID] = append(m.children[inst.info.ParentID], id)
	}
	m.mu.Unlock()
}

// watchWorker monitors a worker's Done channel and handles unexpected exits.
func (m *Manager) watchWorker(instanceID string, done <-chan struct{}) {
	<-done

	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	// Bail out if the instance was removed, stopped, or if the handle was
	// cleared by NewSession (which nils handle before shutting down the old
	// worker to prevent this goroutine from interfering with the new session).
	stale := !ok || inst.info.Status == InstanceStatusStopped || inst.handle == nil
	var name string
	if ok {
		name = inst.info.Name
	}
	m.mu.RUnlock()
	if stale {
		return
	}

	m.logger.Warn("instance process exited unexpectedly",
		"id", instanceID,
		"name", name,
	)

	// Handle the dead instance and its children.
	descendants := m.collectDescendants(instanceID)
	for i := len(descendants) - 1; i >= 0; i-- {
		id := descendants[i]
		m.mu.RLock()
		deadInst, exists := m.instances[id]
		m.mu.RUnlock()
		if !exists || deadInst.info.Status == InstanceStatusStopped {
			continue
		}

		if deadInst.info.Mode.IsPersistent() {
			m.mu.Lock()
			if deadInst.info.Status == InstanceStatusStopped {
				m.mu.Unlock()
				continue
			}
			h := m.detachWorker(deadInst, InstanceStatusStopped)
			m.mu.Unlock()

			m.teardownInstance(id, h, teardownOpts{status: InstanceStatusStopped})
		} else {
			m.mu.Lock()
			h := m.detachWorker(deadInst, "")
			m.unregisterLocked(id, deadInst)
			m.mu.Unlock()

			m.teardownInstance(id, h, teardownOpts{removeDir: true})
		}
	}
}

// watchJobCompletions monitors a worker for background task completions and
// pushes notifications into the instance's notification queue. If the worker
// does not support WatchJobs (e.g. test fakes), this is a no-op.
func (m *Manager) watchJobCompletions(ctx context.Context, worker interface{}, notifications *inference.NotificationQueue) {
	wc, ok := worker.(*grpcipc.WorkerClient)
	if !ok {
		return
	}
	ch := wc.WatchJobs(ctx, m.logger)
	for completion := range ch {
		notifications.Push(formatJobNotification(completion))
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
		summary = fmt.Sprintf("Background command \"%s\" failed with exit code %d", desc, c.ExitCode)
	} else {
		summary = fmt.Sprintf("Background command \"%s\" completed (exit code %d)", desc, c.ExitCode)
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
	m.mu.Lock()
	inst, ok := m.instances[id]
	var h *WorkerHandle
	if ok {
		h = m.detachWorker(inst, "")
	}
	m.unregisterLocked(id, inst)
	m.mu.Unlock()

	if !ok {
		return
	}

	m.teardownInstance(id, h, teardownOpts{
		graceful:  true,
		removeDir: !inst.info.Mode.IsPersistent(),
	})
}
