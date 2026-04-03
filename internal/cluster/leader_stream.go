package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// recvResultBufSize is the channel buffer for the recv goroutine. A buffer
// of 2 ensures the goroutine never blocks after readLoop exits — at most one
// result is in-flight when done fires, plus one more before Recv unblocks.
const recvResultBufSize = 2

// ApprovalStatus represents the result of an atomic approval check.
type ApprovalStatus int

const (
	// ApprovalPending means the node is neither approved nor revoked.
	ApprovalPending ApprovalStatus = iota
	// ApprovalGranted means the node is in the approved list.
	ApprovalGranted
	// ApprovalRevoked means the node has been explicitly revoked.
	ApprovalRevoked
)

// LeaderStream implements the leader side of the Cluster gRPC service.
// It accepts bidirectional streams from worker nodes, verifies their
// identity from the mTLS certificate, and manages per-node connections.
type LeaderStream struct {
	pb.UnimplementedClusterServer

	registry        *NodeRegistry
	checkApproval   func(nodeID string) ApprovalStatus // atomic approval check
	pending         *PendingRegistry
	onNodeConnected func(nodeID NodeID) // called when a node successfully registers
	relayAddr       string              // relay server address (for detecting relay connections)
	relayIPs        map[string]bool     // resolved relay IPs (for matching peer addrs)
	logger          *slog.Logger

	mu    sync.Mutex
	conns map[NodeID]*nodeConn // node ID → active connection
}

// nodeConn represents an active connection to a worker node.
type nodeConn struct {
	nodeID    NodeID
	stream    pb.Cluster_NodeStreamServer
	done      chan struct{} // closed to force disconnect
	closeOnce sync.Once     // prevents double-close panic on done
	sendMu    sync.Mutex    // serialize writes to the stream
	handlers  *NodeHandlers
}

// NodeHandlers holds callbacks for messages received from a node.
type NodeHandlers struct {
	OnSpawnResult     func(nodeID NodeID, msg *pb.SpawnResult)
	OnToolResult      func(nodeID NodeID, msg *pb.NodeToolResult)
	OnWorkerExited    func(nodeID NodeID, msg *pb.WorkerExited)
	OnFileUpdate      func(nodeID NodeID, msg *pb.FileUpdate)
	OnJobCompletion   func(nodeID NodeID, msg *pb.JobCompletionNotify)
	OnTerminalCreated func(nodeID NodeID, msg *pb.TerminalCreated)
	OnTerminalOutput  func(nodeID NodeID, msg *pb.TerminalOutput)
	OnTerminalExited  func(nodeID NodeID, msg *pb.TerminalExited)
}

// NewLeaderStream creates a new leader-side cluster gRPC service.
// checkApproval returns the approval status of a node atomically (approved,
// revoked, or pending) under a single lock, eliminating TOCTOU races.
func NewLeaderStream(registry *NodeRegistry, checkApproval func(string) ApprovalStatus, pending *PendingRegistry, logger *slog.Logger) *LeaderStream {
	return &LeaderStream{
		registry:      registry,
		checkApproval: checkApproval,
		pending:       pending,
		logger:        logger,
		conns:         make(map[NodeID]*nodeConn),
	}
}

// SetRelayAddr sets the relay server address for detecting relay connections.
// Resolves hostname to IPs so we can match against peer addresses.
func (s *LeaderStream) SetRelayAddr(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		s.mu.Lock()
		s.relayAddr = addr
		s.mu.Unlock()
		return
	}
	// Resolve hostname to IPs for reliable comparison with peer addrs.
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		s.mu.Lock()
		s.relayAddr = addr
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.relayIPs = make(map[string]bool, len(ips))
	for _, ip := range ips {
		s.relayIPs[ip] = true
	}
	s.relayAddr = addr
	s.mu.Unlock()
	s.logger.Info("relay address resolved", "addr", addr, "ips", ips)
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
// It extracts the worker's identity from the mTLS certificate, checks
// approval status, and either accepts the node or adds it to the pending list.
func (s *LeaderStream) NodeStream(stream pb.Cluster_NodeStreamServer) error {
	// Step 1: Extract worker identity from mTLS certificate.
	p, ok := peer.FromContext(stream.Context())
	if !ok {
		return fmt.Errorf("no peer info in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return fmt.Errorf("no TLS info in peer")
	}
	pubKey, err := PubKeyFromCert(tlsInfo.State)
	if err != nil {
		return fmt.Errorf("extracting public key from peer cert: %w", err)
	}
	hash := sha256.Sum256(pubKey)
	nodeID := hex.EncodeToString(hash[:])

	peerAddr := p.Addr.String()

	// Step 2: Receive registration message (for node name and capacity).
	msg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving registration: %w", err)
	}
	reg := msg.GetRegister()
	if reg == nil {
		return fmt.Errorf("first message must be NodeRegister")
	}

	// Sanitize node name to prevent oversized values in logs and storage.
	nodeName := reg.NodeName
	if len(nodeName) > maxNodeNameLen {
		nodeName = nodeName[:maxNodeNameLen]
	}

	// Step 3: Check approval / revocation atomically (single lock acquisition).
	status := s.checkApproval(nodeID)

	if status != ApprovalGranted {
		truncID := nodeID
		if len(truncID) > maxNodeIDDisplayLen {
			truncID = truncID[:maxNodeIDDisplayLen] + "..."
		}

		if status == ApprovalRevoked {
			s.logger.Info("rejected revoked node", "node_id", truncID, "name", nodeName)
			_ = stream.Send(&pb.LeaderMessage{
				Msg: &pb.LeaderMessage_Rejected{
					Rejected: &pb.NodeRejected{
						NodeId: nodeID,
						Reason: "approval revoked by leader operator",
					},
				},
			})
			return nil
		}

		// Not approved and not revoked — add to pending list.
		ok, isNew := s.pending.AddOrUpdate(PendingNode{
			NodeID: nodeID,
			Name:   nodeName,
			Addr:   peerAddr,
		})

		if !ok {
			s.logger.Warn("pending registry full, rejecting node", "node_id", truncID)
			_ = stream.Send(&pb.LeaderMessage{
				Msg: &pb.LeaderMessage_Rejected{
					Rejected: &pb.NodeRejected{
						NodeId: nodeID,
						Reason: "pending node limit reached; dismiss or approve existing nodes first",
					},
				},
			})
			return nil
		}

		if isNew {
			s.logger.Info("node pending approval", "node_id", truncID, "name", nodeName, "addr", peerAddr)
		}

		_ = stream.Send(&pb.LeaderMessage{
			Msg: &pb.LeaderMessage_Pending{
				Pending: &pb.NodePending{
					NodeId:  nodeID,
					Message: "awaiting approval from leader operator",
				},
			},
		})
		return nil
	}

	// Step 4: Approved — register and proceed.
	// Remove from pending if it was there (approved while worker was retrying).
	s.pending.Remove(nodeID)

	// Detect relay vs direct connection by checking if peer IP matches relay.
	via := "direct"
	s.mu.Lock()
	hasRelay := s.relayAddr != ""
	relayIPs := s.relayIPs
	s.mu.Unlock()
	if hasRelay {
		peerHost, _, _ := net.SplitHostPort(peerAddr)
		if relayIPs[peerHost] {
			via = "relay"
		}
	}

	if err := s.registry.Register(nodeID, nodeName, int(reg.Capacity), peerAddr, via); err != nil {
		return fmt.Errorf("registering node: %w", err)
	}

	conn := &nodeConn{
		nodeID:   nodeID,
		stream:   stream,
		done:     make(chan struct{}),
		handlers: &NodeHandlers{},
	}

	s.mu.Lock()
	s.conns[nodeID] = conn
	s.mu.Unlock()

	s.logger.Info("node registered", "node_id", nodeID, "name", nodeName, "capacity", reg.Capacity)

	// Send registration confirmation before notifying the service layer.
	// The node must receive Registered before any other messages (FileSync, etc.).
	if err := s.sendToNode(conn, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_Registered{
			Registered: &pb.NodeRegistered{NodeId: nodeID},
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
	if err != nil && !errors.Is(err, io.EOF) {
		s.logger.Warn("node disconnected with error", "node_id", nodeID, "error", err)
		return err
	}
	s.logger.Info("node disconnected", "node_id", nodeID)
	return nil
}

// readLoop processes incoming messages from a node.
// Exits when the stream errors or the conn.done channel is closed.
func (s *LeaderStream) readLoop(conn *nodeConn) error {
	type recvResult struct {
		msg *pb.NodeMessage
		err error
	}
	ch := make(chan recvResult, recvResultBufSize)

	go func() {
		for {
			msg, err := conn.stream.Recv()
			ch <- recvResult{msg, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-conn.done:
			return fmt.Errorf("node disconnected by leader")
		case r := <-ch:
			if r.err != nil {
				return r.err
			}

			// Snapshot handlers under lock so reads are safe against
			// concurrent SetHandlers calls.
			s.mu.Lock()
			h := conn.handlers
			s.mu.Unlock()

			s.registry.Touch(conn.nodeID)

			switch m := r.msg.Msg.(type) {
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

			case *pb.NodeMessage_JobCompletion:
				if h.OnJobCompletion != nil {
					h.OnJobCompletion(conn.nodeID, m.JobCompletion)
				}

			case *pb.NodeMessage_TerminalCreated:
				if h.OnTerminalCreated != nil {
					h.OnTerminalCreated(conn.nodeID, m.TerminalCreated)
				}

			case *pb.NodeMessage_TerminalOutput:
				if h.OnTerminalOutput != nil {
					h.OnTerminalOutput(conn.nodeID, m.TerminalOutput)
				}

			case *pb.NodeMessage_TerminalExited:
				if h.OnTerminalExited != nil {
					h.OnTerminalExited(conn.nodeID, m.TerminalExited)
				}
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

// DisconnectNode forcefully disconnects a node by closing its done channel.
// The readLoop will exit and cleanup will run.
func (s *LeaderStream) DisconnectNode(nodeID NodeID) {
	s.mu.Lock()
	conn, ok := s.conns[nodeID]
	s.mu.Unlock()
	if ok {
		conn.closeOnce.Do(func() { close(conn.done) })
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
