package cluster

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// WorkerStream is the worker-node side of the cluster connection.
// It dials the leader, opens a bidirectional NodeStream, registers,
// and processes incoming commands (spawn, execute tool, shutdown, kill).
type WorkerStream struct {
	leaderAddr string
	nodeName   string
	capacity   int32
	tlsConfig  *tls.Config
	dialFunc   func(ctx context.Context, addr string) (net.Conn, error)
	logger     *slog.Logger

	// Lifecycle callbacks.
	onConnected func() // called after successful registration with leader

	// Handlers for incoming commands from the leader.
	onSpawnWorker    func(ctx context.Context, msg *pb.SpawnWorker)
	onExecuteTool    func(ctx context.Context, msg *pb.ExecuteToolRemote)
	onShutdownWorker func(ctx context.Context, msg *pb.ShutdownWorker)
	onKillWorker     func(ctx context.Context, msg *pb.KillWorker)
	onFileSync       func(ctx context.Context, msg *pb.FileSyncData)
	onFileUpdate     func(ctx context.Context, msg *pb.FileUpdate)

	mu     sync.Mutex
	stream pb.Cluster_NodeStreamClient
	sendMu sync.Mutex
	nodeID string
}

// WorkerStreamConfig configures the worker stream connection.
type WorkerStreamConfig struct {
	LeaderAddr string
	NodeName   string
	Capacity   int32
	TLSConfig  *tls.Config                                              // if set, use mTLS; otherwise plaintext
	DialFunc   func(ctx context.Context, addr string) (net.Conn, error) // optional custom dialer (e.g. relay)
	Logger     *slog.Logger
}

// NewWorkerStream creates a new worker-node stream client.
func NewWorkerStream(cfg WorkerStreamConfig) *WorkerStream {
	return &WorkerStream{
		leaderAddr: cfg.LeaderAddr,
		nodeName:   cfg.NodeName,
		capacity:   cfg.Capacity,
		tlsConfig:  cfg.TLSConfig,
		dialFunc:   cfg.DialFunc,
		logger:     cfg.Logger,
	}
}

// LeaderAddr returns the current leader address.
func (w *WorkerStream) LeaderAddr() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.leaderAddr
}

// SetLeaderAddr updates the leader address for the next connection attempt.
func (w *WorkerStream) SetLeaderAddr(addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.leaderAddr = addr
}

// SetSpawnHandler sets the callback for SpawnWorker commands.
func (w *WorkerStream) SetSpawnHandler(fn func(ctx context.Context, msg *pb.SpawnWorker)) {
	w.onSpawnWorker = fn
}

// SetExecuteToolHandler sets the callback for ExecuteToolRemote commands.
func (w *WorkerStream) SetExecuteToolHandler(fn func(ctx context.Context, msg *pb.ExecuteToolRemote)) {
	w.onExecuteTool = fn
}

// SetShutdownWorkerHandler sets the callback for ShutdownWorker commands.
func (w *WorkerStream) SetShutdownWorkerHandler(fn func(ctx context.Context, msg *pb.ShutdownWorker)) {
	w.onShutdownWorker = fn
}

// SetKillWorkerHandler sets the callback for KillWorker commands.
func (w *WorkerStream) SetKillWorkerHandler(fn func(ctx context.Context, msg *pb.KillWorker)) {
	w.onKillWorker = fn
}

// SetFileSyncHandler sets the callback for FileSyncData messages.
func (w *WorkerStream) SetFileSyncHandler(fn func(ctx context.Context, msg *pb.FileSyncData)) {
	w.onFileSync = fn
}

// SetOnConnected sets a callback invoked after successful registration with the leader.
func (w *WorkerStream) SetOnConnected(fn func()) {
	w.onConnected = fn
}

// SetFileUpdateHandler sets the callback for FileUpdate messages.
func (w *WorkerStream) SetFileUpdateHandler(fn func(ctx context.Context, msg *pb.FileUpdate)) {
	w.onFileUpdate = fn
}

// Connect dials the leader, registers, and enters the message loop.
// Blocks until the context is cancelled or the connection drops.
func (w *WorkerStream) Connect(ctx context.Context) error {
	if w.tlsConfig == nil {
		return fmt.Errorf("TLS config is required for cluster connections")
	}
	creds := credentials.NewTLS(w.tlsConfig)

	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if w.dialFunc != nil {
		opts = append(opts, grpc.WithContextDialer(w.dialFunc))
	}

	conn, err := grpc.NewClient(w.leaderAddr, opts...)
	if err != nil {
		return fmt.Errorf("dialing leader at %s: %w", w.leaderAddr, err)
	}
	defer conn.Close()

	client := pb.NewClusterClient(conn)
	stream, err := client.NodeStream(ctx)
	if err != nil {
		return fmt.Errorf("opening node stream: %w", err)
	}

	w.mu.Lock()
	w.stream = stream
	w.mu.Unlock()

	// Register with the leader.
	if err := w.send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Register{
			Register: &pb.NodeRegister{
				NodeName: w.nodeName,
				Capacity: w.capacity,
			},
		},
	}); err != nil {
		return fmt.Errorf("sending registration: %w", err)
	}

	// Wait for registration response — could be accepted or pending approval.
	msg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving registration response: %w", err)
	}
	switch resp := msg.Msg.(type) {
	case *pb.LeaderMessage_Registered:
		w.nodeID = resp.Registered.NodeId
		w.logger.Info("registered with leader", "node_id", w.nodeID, "leader", w.leaderAddr)
		if w.onConnected != nil {
			w.onConnected()
		}
	case *pb.LeaderMessage_Pending:
		return ErrPendingApproval
	default:
		return fmt.Errorf("expected NodeRegistered or NodePending, got %T", msg.Msg)
	}

	// Enter message loop.
	return w.readLoop(ctx, stream)
}

// readLoop processes incoming messages from the leader.
// maxConcurrentHandlers limits the number of goroutines handling incoming
// commands (spawn, execute, shutdown, kill) to prevent unbounded growth.
const maxConcurrentHandlers = 64

func (w *WorkerStream) readLoop(ctx context.Context, stream pb.Cluster_NodeStreamClient) error {
	sem := make(chan struct{}, maxConcurrentHandlers)
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receiving from leader: %w", err)
		}

		switch m := msg.Msg.(type) {
		case *pb.LeaderMessage_SpawnWorker:
			if w.onSpawnWorker != nil {
				go func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					w.onSpawnWorker(ctx, m.SpawnWorker)
				}()
			}

		case *pb.LeaderMessage_ExecuteTool:
			if w.onExecuteTool != nil {
				go func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					w.onExecuteTool(ctx, m.ExecuteTool)
				}()
			}

		case *pb.LeaderMessage_ShutdownWorker:
			if w.onShutdownWorker != nil {
				go func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					w.onShutdownWorker(ctx, m.ShutdownWorker)
				}()
			}

		case *pb.LeaderMessage_KillWorker:
			if w.onKillWorker != nil {
				go func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					w.onKillWorker(ctx, m.KillWorker)
				}()
			}

		case *pb.LeaderMessage_FileSync:
			if w.onFileSync != nil {
				w.onFileSync(ctx, m.FileSync)
			}

		case *pb.LeaderMessage_FileUpdate:
			if w.onFileUpdate != nil {
				w.onFileUpdate(ctx, m.FileUpdate)
			}
		}
	}
}

// Send sends a message to the leader with write serialization.
func (w *WorkerStream) Send(msg *pb.NodeMessage) error {
	return w.send(msg)
}

func (w *WorkerStream) send(msg *pb.NodeMessage) error {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	w.mu.Lock()
	s := w.stream
	w.mu.Unlock()
	if s == nil {
		return fmt.Errorf("not connected")
	}
	return s.Send(msg)
}

// SendSpawnResult sends a spawn result back to the leader.
func (w *WorkerStream) SendSpawnResult(requestID, errMsg string) error {
	return w.send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_SpawnResult{
			SpawnResult: &pb.SpawnResult{
				RequestId: requestID,
				Error:     errMsg,
			},
		},
	})
}

// SendToolResult sends a tool execution result back to the leader.
func (w *WorkerStream) SendToolResult(sessionID, callID, content string, isError bool, errMsg string) error {
	return w.send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_ToolResult{
			ToolResult: &pb.NodeToolResult{
				CallId:    callID,
				Content:   content,
				IsError:   isError,
				Error:     errMsg,
				SessionId: sessionID,
			},
		},
	})
}

// SendWorkerExited notifies the leader that a local worker process exited.
func (w *WorkerStream) SendWorkerExited(sessionID, errMsg string) error {
	return w.send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_WorkerExited{
			WorkerExited: &pb.WorkerExited{
				SessionId: sessionID,
				Error:     errMsg,
			},
		},
	})
}

// SendHeartbeat sends a heartbeat to the leader.
func (w *WorkerStream) SendHeartbeat(activeWorkers int32) error {
	return w.send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Heartbeat{
			Heartbeat: &pb.NodeHeartbeat{
				ActiveWorkers: activeWorkers,
			},
		},
	})
}

// NodeID returns the node ID assigned by the leader after registration.
func (w *WorkerStream) NodeID() string {
	return w.nodeID
}
