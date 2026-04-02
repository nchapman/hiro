package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

const spawnTimeout = 30 * time.Second

// LeaderService orchestrates cluster operations from the leader side.
// It manages node connections, routes spawn requests to remote nodes,
// and creates RemoteWorker handles for the Manager.
type LeaderService struct {
	stream   *LeaderStream
	registry *NodeRegistry
	logger   *slog.Logger

	mu         sync.Mutex
	workers    map[string]*RemoteWorker // session ID → remote worker
	spawnChans map[string]chan string   // request ID → spawn result channel

	fileSync *FileSyncService // for sending file updates to nodes; nil if not configured

	onJobCompletion  func(sessionID string, completion *pb.JobCompletionNotify) // called on background task completion
	onTerminalCreated func(nodeID NodeID, msg *pb.TerminalCreated)
	onTerminalOutput  func(nodeID NodeID, msg *pb.TerminalOutput)
	onTerminalExited  func(nodeID NodeID, msg *pb.TerminalExited)
}

// NewLeaderService creates a new cluster leader service.
func NewLeaderService(stream *LeaderStream, registry *NodeRegistry, logger *slog.Logger) *LeaderService {
	svc := &LeaderService{
		stream:     stream,
		registry:   registry,
		logger:     logger.With("component", "cluster"),
		workers:    make(map[string]*RemoteWorker),
		spawnChans: make(map[string]chan string),
	}

	// Auto-wire handlers and send initial sync when nodes connect.
	stream.SetOnNodeConnected(func(nodeID NodeID) {
		svc.WireNodeHandlers(nodeID)
		if svc.getFileSync() != nil {
			go svc.sendInitialSync(nodeID)
		}
	})

	return svc
}

// Stream returns the underlying LeaderStream for gRPC registration.
func (s *LeaderService) Stream() *LeaderStream {
	return s.stream
}

// Registry returns the node registry.
func (s *LeaderService) Registry() *NodeRegistry {
	return s.registry
}

// WireNodeHandlers installs stable handlers for a node. Called once when
// a node first connects. These handlers never get replaced — spawn results,
// tool results, and worker exits all route through the LeaderService maps.
func (s *LeaderService) WireNodeHandlers(nodeID NodeID) {
	s.stream.SetHandlers(nodeID, &NodeHandlers{
		OnSpawnResult: func(_ NodeID, msg *pb.SpawnResult) {
			s.mu.Lock()
			ch, ok := s.spawnChans[msg.RequestId]
			if ok {
				delete(s.spawnChans, msg.RequestId)
			}
			s.mu.Unlock()
			if ok {
				ch <- msg.Error
			}
		},
		OnToolResult: func(_ NodeID, msg *pb.NodeToolResult) {
			s.deliverToolResult(msg)
		},
		OnWorkerExited: func(_ NodeID, msg *pb.WorkerExited) {
			s.handleWorkerExited(msg)
		},
		OnFileUpdate: func(fromNode NodeID, msg *pb.FileUpdate) {
			s.handleFileUpdate(fromNode, msg)
		},
		OnJobCompletion: func(_ NodeID, msg *pb.JobCompletionNotify) {
			if s.onJobCompletion != nil {
				s.onJobCompletion(msg.SessionId, msg)
			}
		},
		OnTerminalCreated: func(nodeID NodeID, msg *pb.TerminalCreated) {
			if s.onTerminalCreated != nil {
				s.onTerminalCreated(nodeID, msg)
			}
		},
		OnTerminalOutput: func(nodeID NodeID, msg *pb.TerminalOutput) {
			if s.onTerminalOutput != nil {
				s.onTerminalOutput(nodeID, msg)
			}
		},
		OnTerminalExited: func(nodeID NodeID, msg *pb.TerminalExited) {
			if s.onTerminalExited != nil {
				s.onTerminalExited(nodeID, msg)
			}
		},
	})
}

// SpawnOnNode spawns a worker on a remote node. Returns an ipc.AgentWorker
// (RemoteWorker) and control functions. Safe for concurrent use — multiple
// spawns on the same node do not interfere with each other.
func (s *LeaderService) SpawnOnNode(ctx context.Context, nodeID NodeID, cfg SpawnRequest) (*RemoteWorkerHandle, error) {
	// Verify node is connected.
	node, ok := s.registry.Get(nodeID)
	if !ok {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}
	if node.Status != NodeOnline {
		return nil, fmt.Errorf("node %s is offline", nodeID)
	}

	// Create the remote worker adapter.
	rw := NewRemoteWorker(s.stream, nodeID, cfg.SessionID)

	// Register per-request spawn channel before sending.
	requestID := cfg.SessionID
	spawnCh := make(chan string, 1)

	s.mu.Lock()
	s.workers[cfg.SessionID] = rw
	s.spawnChans[requestID] = spawnCh
	s.mu.Unlock()

	// Send spawn request.
	err := s.stream.SendToNode(nodeID, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_SpawnWorker{
			SpawnWorker: &pb.SpawnWorker{
				RequestId:      requestID,
				InstanceId:     cfg.InstanceID,
				SessionId:      cfg.SessionID,
				AgentName:      cfg.AgentName,
				EffectiveTools: cfg.EffectiveTools,
				WorkingDir:     cfg.WorkingDir,
				SessionDir:     cfg.SessionDir,
			},
		},
	})
	if err != nil {
		s.cleanupSpawn(cfg.SessionID, requestID)
		rw.Close()
		return nil, fmt.Errorf("sending spawn request to node %s: %w", nodeID, err)
	}

	// Wait for spawn result.
	select {
	case errMsg := <-spawnCh:
		if errMsg != "" {
			s.cleanupSpawn(cfg.SessionID, requestID)
			rw.Close()
			return nil, fmt.Errorf("remote spawn on node %s failed: %s", nodeID, errMsg)
		}
	case <-time.After(spawnTimeout):
		s.cleanupSpawn(cfg.SessionID, requestID)
		rw.Close()
		return nil, fmt.Errorf("remote spawn on node %s timed out", nodeID)
	case <-ctx.Done():
		s.cleanupSpawn(cfg.SessionID, requestID)
		rw.Close()
		return nil, ctx.Err()
	}

	s.registry.IncrementActive(nodeID)
	s.logger.Info("remote worker spawned",
		"node", nodeID,
		"instance", cfg.InstanceID,
		"session", cfg.SessionID,
		"agent", cfg.AgentName,
	)

	return &RemoteWorkerHandle{
		Worker: rw,
		NodeID: nodeID,
		Kill: func() {
			rw.Kill()
		},
		Close: func() {
			s.removeWorker(cfg.SessionID)
			s.registry.DecrementActive(nodeID)
			rw.Close()
		},
		Done: rw.Done(),
	}, nil
}

// deliverToolResult routes a tool result directly to the owning RemoteWorker
// by session ID (no scanning).
func (s *LeaderService) deliverToolResult(msg *pb.NodeToolResult) {
	s.mu.Lock()
	rw, ok := s.workers[msg.SessionId]
	s.mu.Unlock()

	if !ok {
		return
	}

	var transportErr error
	if msg.Error != "" {
		transportErr = fmt.Errorf("remote tool error: %s", msg.Error)
	}

	rw.DeliverToolResult(msg.CallId, ipc.ToolResult{
		Content: msg.Content,
		IsError: msg.IsError,
	}, transportErr)
}

// handleWorkerExited handles notification that a remote worker exited.
func (s *LeaderService) handleWorkerExited(msg *pb.WorkerExited) {
	s.mu.Lock()
	rw, ok := s.workers[msg.SessionId]
	s.mu.Unlock()

	if ok {
		s.logger.Info("remote worker exited", "session", msg.SessionId, "error", msg.Error)
		rw.Close()
		s.removeWorker(msg.SessionId)
	}
}

func (s *LeaderService) removeWorker(sessionID string) {
	s.mu.Lock()
	delete(s.workers, sessionID)
	s.mu.Unlock()
}

func (s *LeaderService) cleanupSpawn(sessionID, requestID string) {
	s.mu.Lock()
	delete(s.workers, sessionID)
	delete(s.spawnChans, requestID)
	s.mu.Unlock()
}

// SetJobCompletionHandler sets the callback invoked when a remote worker
// reports a background task completion. The manager uses this to push
// notifications into the correct instance's notification queue.
func (s *LeaderService) SetJobCompletionHandler(fn func(sessionID string, completion *pb.JobCompletionNotify)) {
	s.mu.Lock()
	s.onJobCompletion = fn
	s.mu.Unlock()
}

// SetTerminalHandlers sets callbacks for terminal messages from worker nodes.
func (s *LeaderService) SetTerminalHandlers(
	onCreated func(nodeID NodeID, msg *pb.TerminalCreated),
	onOutput func(nodeID NodeID, msg *pb.TerminalOutput),
	onExited func(nodeID NodeID, msg *pb.TerminalExited),
) {
	s.mu.Lock()
	s.onTerminalCreated = onCreated
	s.onTerminalOutput = onOutput
	s.onTerminalExited = onExited
	s.mu.Unlock()
}

// SendTerminalMessage sends a terminal-related message to a specific node.
func (s *LeaderService) SendTerminalMessage(nodeID NodeID, msg *pb.LeaderMessage) error {
	return s.stream.SendToNode(nodeID, msg)
}

// SetFileSync configures the leader's file sync service. When set, new nodes
// receive an initial file sync on connect and incremental updates thereafter.
func (s *LeaderService) SetFileSync(fs *FileSyncService) {
	s.mu.Lock()
	s.fileSync = fs
	s.mu.Unlock()
}

// sendInitialSync sends the initial file sync (zstd tar) to a newly
// connected node, then runs a reconciliation pass to catch any files
// that changed during the tar creation.
func (s *LeaderService) sendInitialSync(nodeID NodeID) {
	fs := s.getFileSync()
	if fs == nil {
		return
	}
	data, err := fs.CreateInitialSync()
	if err != nil {
		s.logger.Error("failed to create initial sync, node will rely on incremental sync", "node", nodeID, "error", err)
		// Send empty Final:true so the node doesn't block waiting for sync.
		_ = s.stream.SendToNode(nodeID, &pb.LeaderMessage{
			Msg: &pb.LeaderMessage_FileSync{FileSync: &pb.FileSyncData{Final: true}},
		})
		return
	}

	// Send in chunks. Always send at least one chunk (even if empty)
	// so the node receives a Final:true signal to complete initial sync.
	if len(data) == 0 {
		if err := s.stream.SendToNode(nodeID, &pb.LeaderMessage{
			Msg: &pb.LeaderMessage_FileSync{
				FileSync: &pb.FileSyncData{Data: nil, Final: true},
			},
		}); err != nil {
			s.logger.Error("failed to send empty file sync", "node", nodeID, "error", err)
			return
		}
	} else {
		for i := 0; i < len(data); i += initialSyncChunkSize {
			end := i + initialSyncChunkSize
			isFinal := false
			if end >= len(data) {
				end = len(data)
				isFinal = true
			}
			if err := s.stream.SendToNode(nodeID, &pb.LeaderMessage{
				Msg: &pb.LeaderMessage_FileSync{
					FileSync: &pb.FileSyncData{
						Data:  data[i:end],
						Final: isFinal,
					},
				},
			}); err != nil {
				s.logger.Error("failed to send file sync chunk", "node", nodeID, "error", err)
				return
			}
		}
	}

	s.logger.Info("initial file sync sent", "node", nodeID, "size", len(data))

	// Reconcile catches files that changed between CreateInitialSync() and
	// the node finishing extraction. Updates are sent via the normal sendFn
	// which broadcasts to all connected nodes — existing nodes ignore files
	// they already have (echo suppression + mtime check).
	if err := fs.Reconcile(nil); err != nil {
		s.logger.Warn("reconciliation after initial sync failed", "node", nodeID, "error", err)
	}
}

// getFileSync returns the file sync service, protected by the mutex.
func (s *LeaderService) getFileSync() *FileSyncService {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileSync
}

// handleFileUpdate processes an incoming file update from a worker node.
// It applies the change to the leader's filesystem and re-broadcasts to
// other connected nodes (excluding the sender).
func (s *LeaderService) handleFileUpdate(fromNode NodeID, msg *pb.FileUpdate) {
	fs := s.getFileSync()
	if fs == nil {
		return
	}
	// Stamp the authenticated node ID to prevent OriginNode spoofing.
	msg.OriginNode = string(fromNode)
	if err := fs.ApplyFileUpdate(msg); err != nil {
		s.logger.Warn("failed to apply file update from node", "node", fromNode, "path", msg.Path, "error", err)
		return
	}
	s.logger.Debug("applied file update from node", "node", fromNode, "path", msg.Path)

	// Re-broadcast to other nodes (not back to the sender).
	for _, nodeID := range s.stream.ConnectedNodes() {
		if nodeID == fromNode {
			continue
		}
		if err := s.stream.SendToNode(nodeID, &pb.LeaderMessage{
			Msg: &pb.LeaderMessage_FileUpdate{FileUpdate: msg},
		}); err != nil {
			s.logger.Debug("failed to forward file update to node", "node", nodeID, "error", err)
		}
	}
}

// BroadcastFileUpdate sends a file update to all connected nodes.
func (s *LeaderService) BroadcastFileUpdate(update *pb.FileUpdate) {
	for _, nodeID := range s.stream.ConnectedNodes() {
		if err := s.stream.SendToNode(nodeID, &pb.LeaderMessage{
			Msg: &pb.LeaderMessage_FileUpdate{FileUpdate: update},
		}); err != nil {
			s.logger.Debug("failed to send file update to node", "node", nodeID, "error", err)
		}
	}
}

// KillWorkersOnNode sends KillWorker to all active workers on a given node
// and removes them from the workers map. Called before disconnecting a revoked
// node to ensure in-flight work is stopped promptly.
func (s *LeaderService) KillWorkersOnNode(nodeID NodeID) {
	s.mu.Lock()
	var toKill []*RemoteWorker
	var sessionIDs []string
	for sid, rw := range s.workers {
		if rw.NodeID() == nodeID {
			toKill = append(toKill, rw)
			sessionIDs = append(sessionIDs, sid)
		}
	}
	for _, sid := range sessionIDs {
		delete(s.workers, sid)
	}
	s.mu.Unlock()

	for _, rw := range toKill {
		rw.Kill()
		rw.Close()
	}
	if len(toKill) > 0 {
		s.logger.Info("killed workers on disconnected node", "node", nodeID, "count", len(toKill))
	}
}

// SpawnRequest contains the information needed to spawn a worker on a remote node.
type SpawnRequest struct {
	InstanceID     string
	SessionID      string
	AgentName      string
	EffectiveTools map[string]bool
	WorkingDir     string // relative path within HIRO_ROOT
	SessionDir     string // relative path within HIRO_ROOT
}

// RemoteWorkerHandle wraps a RemoteWorker with control functions,
// matching the shape of agent.WorkerHandle.
type RemoteWorkerHandle struct {
	Worker ipc.AgentWorker // the RemoteWorker (implements ipc.AgentWorker)
	NodeID NodeID
	Kill   func()
	Close  func()
	Done   <-chan struct{}
}
