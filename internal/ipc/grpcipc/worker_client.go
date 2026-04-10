package grpcipc

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
)

// WorkerClient implements ipc.AgentWorker by making gRPC calls to an AgentWorker server.
type WorkerClient struct {
	client      pb.AgentWorkerClient
	secretEnvFn func() []string // returns secret env vars for bash injection
}

// NewWorkerClient creates an AgentWorker backed by a gRPC connection.
func NewWorkerClient(cc grpc.ClientConnInterface) *WorkerClient {
	return &WorkerClient{client: pb.NewAgentWorkerClient(cc)}
}

// SetSecretEnvFn sets the function that provides secret env vars.
// Called by the control plane after construction. Secrets are only
// injected into Bash tool calls.
func (c *WorkerClient) SetSecretEnvFn(fn func() []string) {
	c.secretEnvFn = fn
}

func (c *WorkerClient) ExecuteTool(ctx context.Context, callID, name, input string) (ipc.ToolResult, error) {
	req := &pb.ExecuteToolRequest{
		CallId: callID,
		Name:   name,
		Input:  input,
	}
	if c.secretEnvFn != nil && ipc.NeedsSecrets(name) {
		req.SecretEnv = c.secretEnvFn()
	}
	resp, err := c.client.ExecuteTool(ctx, req)
	if err != nil {
		return ipc.ToolResult{}, err
	}
	return ipc.ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}

func (c *WorkerClient) Shutdown(ctx context.Context) error {
	_, err := c.client.Shutdown(ctx, &pb.ShutdownRequest{})
	return err
}

// WatchJobs opens a streaming RPC to receive background job completion events.
// Returns a channel that receives completions. The channel is closed when the
// stream ends (worker shutdown, disconnect, or context cancellation).
// This is a concrete method on WorkerClient, not part of the AgentWorker interface.
func (c *WorkerClient) WatchJobs(ctx context.Context, logger *slog.Logger) <-chan *pb.JobCompletion {
	if logger == nil {
		logger = slog.Default()
	}
	const jobChannelBuffer = 64
	ch := make(chan *pb.JobCompletion, jobChannelBuffer)
	go func() {
		defer close(ch)
		stream, err := c.client.WatchJobs(ctx, &pb.WatchJobsRequest{})
		if err != nil {
			logger.Debug("failed to open WatchJobs stream", "error", err)
			return
		}
		for {
			completion, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					logger.Debug("WatchJobs stream ended", "error", err)
				}
				return
			}
			select {
			case ch <- completion:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
