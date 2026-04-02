package cluster

import (
	"context"
	"fmt"
	"sync"

	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

// RemoteWorker implements ipc.AgentWorker by forwarding tool calls over
// the cluster gRPC stream to a worker node. The leader creates one per
// remote instance. To the inference loop it looks identical to a local
// gRPC worker client.
type RemoteWorker struct {
	leader     *LeaderStream
	nodeID     NodeID
	sessionID  string

	secretEnvFn func() []string // provides secrets for each tool call

	mu      sync.Mutex
	pending map[string]chan toolResponse // callID → result channel
	done    chan struct{}
	closed  bool
}

type toolResponse struct {
	result ipc.ToolResult
	err    error
}

// NewRemoteWorker creates a remote worker adapter for a specific instance
// running on a remote node.
func NewRemoteWorker(leader *LeaderStream, nodeID NodeID, sessionID string) *RemoteWorker {
	rw := &RemoteWorker{
		leader:    leader,
		nodeID:    nodeID,
		sessionID: sessionID,
		pending:   make(map[string]chan toolResponse),
		done:      make(chan struct{}),
	}

	// Register handlers to receive results for this worker's calls.
	// The LeaderStream dispatches by node; we filter by callID in pending map.
	return rw
}

// NodeID returns the ID of the node this worker is running on.
func (rw *RemoteWorker) NodeID() NodeID {
	return rw.nodeID
}

// SetSecretEnvFn sets the function that provides secret env vars.
// Secrets are sent with each ExecuteTool call.
func (rw *RemoteWorker) SetSecretEnvFn(fn func() []string) {
	rw.secretEnvFn = fn
}

// ExecuteTool sends a tool execution request to the remote node and blocks
// until the result arrives or the context expires.
func (rw *RemoteWorker) ExecuteTool(ctx context.Context, callID, name, input string) (ipc.ToolResult, error) {
	rw.mu.Lock()
	if rw.closed {
		rw.mu.Unlock()
		return ipc.ToolResult{}, fmt.Errorf("remote worker closed")
	}

	ch := make(chan toolResponse, 1)
	rw.pending[callID] = ch
	rw.mu.Unlock()

	defer func() {
		rw.mu.Lock()
		delete(rw.pending, callID)
		rw.mu.Unlock()
	}()

	// Build the request.
	req := &pb.ExecuteToolRemote{
		SessionId: rw.sessionID,
		CallId:    callID,
		ToolName:  name,
		Input:     input,
	}
	if rw.secretEnvFn != nil {
		req.SecretEnv = rw.secretEnvFn()
	}

	// Send to the node.
	if err := rw.leader.SendToNode(rw.nodeID, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ExecuteTool{
			ExecuteTool: req,
		},
	}); err != nil {
		return ipc.ToolResult{}, fmt.Errorf("sending tool call to node %s: %w", rw.nodeID, err)
	}

	// Wait for result.
	select {
	case resp := <-ch:
		return resp.result, resp.err
	case <-rw.done:
		return ipc.ToolResult{}, fmt.Errorf("remote worker disconnected")
	case <-ctx.Done():
		return ipc.ToolResult{}, ctx.Err()
	}
}

// Shutdown sends a shutdown request to the remote node for this worker.
func (rw *RemoteWorker) Shutdown(ctx context.Context) error {
	return rw.leader.SendToNode(rw.nodeID, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ShutdownWorker{
			ShutdownWorker: &pb.ShutdownWorker{
				SessionId: rw.sessionID,
			},
		},
	})
}

// Kill sends a kill request to the remote node for this worker.
func (rw *RemoteWorker) Kill() {
	_ = rw.leader.SendToNode(rw.nodeID, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_KillWorker{
			KillWorker: &pb.KillWorker{
				SessionId: rw.sessionID,
			},
		},
	})
}

// Close marks this remote worker as closed and unblocks any pending calls.
func (rw *RemoteWorker) Close() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.closed {
		return
	}
	rw.closed = true
	close(rw.done)
}

// Done returns a channel that is closed when the remote worker is closed.
func (rw *RemoteWorker) Done() <-chan struct{} {
	return rw.done
}

// DeliverToolResult delivers a tool result from the node to the pending
// call. Called by the LeaderService when a NodeToolResult arrives.
func (rw *RemoteWorker) DeliverToolResult(callID string, result ipc.ToolResult, err error) {
	rw.mu.Lock()
	ch, ok := rw.pending[callID]
	rw.mu.Unlock()

	if ok {
		ch <- toolResponse{result: result, err: err}
	}
}
