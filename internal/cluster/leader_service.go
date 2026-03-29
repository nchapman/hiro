package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
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
}

// NewLeaderService creates a new cluster leader service.
func NewLeaderService(stream *LeaderStream, registry *NodeRegistry, logger *slog.Logger) *LeaderService {
	return &LeaderService{
		stream:     stream,
		registry:   registry,
		logger:     logger,
		workers:    make(map[string]*RemoteWorker),
		spawnChans: make(map[string]chan string),
	}
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

// SpawnRequest contains the information needed to spawn a worker on a remote node.
type SpawnRequest struct {
	InstanceID     string
	SessionID      string
	AgentName      string
	EffectiveTools map[string]bool
	WorkingDir     string // relative path within HIVE_ROOT
	SessionDir     string // relative path within HIVE_ROOT
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
