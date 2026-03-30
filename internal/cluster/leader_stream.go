package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sync"

	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
)

// LeaderStream implements the leader side of the Cluster gRPC service.
// It accepts bidirectional streams from worker nodes, validates
// registration, and manages per-node connections.
type LeaderStream struct {
	pb.UnimplementedClusterServer

	registry       *NodeRegistry
	validateToken  func(token string) string // returns token name or "" if invalid
	onNodeConnected func(nodeID NodeID)       // called when a node successfully registers
	logger         *slog.Logger

	mu    sync.Mutex
	conns map[NodeID]*nodeConn // node ID → active connection
}

// nodeConn represents an active connection to a worker node.
type nodeConn struct {
	nodeID   NodeID
	stream   pb.Cluster_NodeStreamServer
	sendMu   sync.Mutex // serialize writes to the stream
	handlers *NodeHandlers
}

// NodeHandlers holds callbacks for messages received from a node.
type NodeHandlers struct {
	OnSpawnResult  func(nodeID NodeID, msg *pb.SpawnResult)
	OnToolResult   func(nodeID NodeID, msg *pb.NodeToolResult)
	OnWorkerExited func(nodeID NodeID, msg *pb.WorkerExited)
	OnFileUpdate   func(nodeID NodeID, msg *pb.FileUpdate)
}

// NewLeaderStream creates a new leader-side cluster gRPC service.
func NewLeaderStream(registry *NodeRegistry, validateToken func(string) string, logger *slog.Logger) *LeaderStream {
	return &LeaderStream{
		registry:      registry,
		validateToken: validateToken,
		logger:        logger,
		conns:         make(map[NodeID]*nodeConn),
	}
}

// SetOnNodeConnected sets a callback invoked when a node successfully registers.
func (s *LeaderStream) SetOnNodeConnected(fn func(nodeID NodeID)) {
	s.onNodeConnected = fn
}

// Register registers this service with a gRPC server.
func (s *LeaderStream) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterClusterServer(registrar, s)
}

// NodeStream handles a bidirectional stream from a worker node.
// The first message must be a NodeRegister. After successful registration,
// the node enters a message loop handling tool results, spawn results,
// heartbeats, and worker exit notifications.
func (s *LeaderStream) NodeStream(stream pb.Cluster_NodeStreamServer) error {
	// First message must be registration.
	msg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving registration: %w", err)
	}

	reg := msg.GetRegister()
	if reg == nil {
		return fmt.Errorf("first message must be NodeRegister")
	}

	// Validate token.
	tokenName := s.validateToken(reg.JoinToken)
	if tokenName == "" {
		return fmt.Errorf("invalid join token")
	}

	// Generate unique node ID with random suffix to prevent collisions
	// on reconnect or duplicate names.
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("generating node ID: %w", err)
	}
	nodeID := NodeID(fmt.Sprintf("node-%s-%s", reg.NodeName, hex.EncodeToString(suffix)))
	if err := s.registry.Register(nodeID, reg.NodeName, int(reg.Capacity)); err != nil {
		return fmt.Errorf("registering node: %w", err)
	}

	conn := &nodeConn{
		nodeID:   nodeID,
		stream:   stream,
		handlers: &NodeHandlers{},
	}

	s.mu.Lock()
	s.conns[nodeID] = conn
	s.mu.Unlock()

	s.logger.Info("node registered", "node_id", nodeID, "name", reg.NodeName, "capacity", reg.Capacity, "token", tokenName)

	// Send registration confirmation before notifying the service layer.
	// The node must receive Registered before any other messages (FileSync, etc.).
	if err := s.sendToNode(conn, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_Registered{
			Registered: &pb.NodeRegistered{NodeId: string(nodeID)},
		},
	}); err != nil {
		s.cleanupNode(nodeID)
		return fmt.Errorf("sending registered: %w", err)
	}

	// Notify the service layer so it can wire up handlers and send initial sync.
	if s.onNodeConnected != nil {
		s.onNodeConnected(nodeID)
	}

	// Enter message loop.
	err = s.readLoop(conn)

	// Cleanup on disconnect.
	s.cleanupNode(nodeID)
	if err != nil && err != io.EOF {
		s.logger.Warn("node disconnected with error", "node_id", nodeID, "error", err)
		return err
	}
	s.logger.Info("node disconnected", "node_id", nodeID)
	return nil
}

// readLoop processes incoming messages from a node.
func (s *LeaderStream) readLoop(conn *nodeConn) error {
	for {
		msg, err := conn.stream.Recv()
		if err != nil {
			return err
		}

		// Snapshot handlers under lock so reads are safe against
		// concurrent SetHandlers calls.
		s.mu.Lock()
		h := conn.handlers
		s.mu.Unlock()

		s.registry.Touch(conn.nodeID)

		switch m := msg.Msg.(type) {
		case *pb.NodeMessage_SpawnResult:
			if h.OnSpawnResult != nil {
				h.OnSpawnResult(conn.nodeID, m.SpawnResult)
			}

		case *pb.NodeMessage_ToolResult:
			if h.OnToolResult != nil {
				h.OnToolResult(conn.nodeID, m.ToolResult)
			}

		case *pb.NodeMessage_Heartbeat:
			// Touch already called above.

		case *pb.NodeMessage_WorkerExited:
			if h.OnWorkerExited != nil {
				h.OnWorkerExited(conn.nodeID, m.WorkerExited)
			}

		case *pb.NodeMessage_FileUpdate:
			if h.OnFileUpdate != nil {
				h.OnFileUpdate(conn.nodeID, m.FileUpdate)
			}
		}
	}
}

// cleanupNode removes a node from the registry and connection map.
func (s *LeaderStream) cleanupNode(nodeID NodeID) {
	s.mu.Lock()
	delete(s.conns, nodeID)
	s.mu.Unlock()
	s.registry.SetOffline(nodeID)
}

// SendToNode sends a message to a specific node. Returns an error if the
// node is not connected.
func (s *LeaderStream) SendToNode(nodeID NodeID, msg *pb.LeaderMessage) error {
	s.mu.Lock()
	conn, ok := s.conns[nodeID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("node %s not connected", nodeID)
	}
	return s.sendToNode(conn, msg)
}

// sendToNode sends a message to a node connection with write serialization.
func (s *LeaderStream) sendToNode(conn *nodeConn, msg *pb.LeaderMessage) error {
	conn.sendMu.Lock()
	defer conn.sendMu.Unlock()
	return conn.stream.Send(msg)
}

// SetHandlers sets message handlers for a specific node.
func (s *LeaderStream) SetHandlers(nodeID NodeID, handlers *NodeHandlers) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn, ok := s.conns[nodeID]; ok {
		conn.handlers = handlers
	}
}

// ConnectedNodes returns the IDs of all connected nodes (excluding home).
func (s *LeaderStream) ConnectedNodes() []NodeID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]NodeID, 0, len(s.conns))
	for id := range s.conns {
		ids = append(ids, id)
	}
	return ids
}
