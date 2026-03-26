package grpcipc

import (
	"context"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
)

// WorkerClient implements ipc.AgentWorker by making gRPC calls to an AgentWorker server.
type WorkerClient struct {
	client pb.AgentWorkerClient
}

// NewWorkerClient creates an AgentWorker backed by a gRPC connection.
func NewWorkerClient(cc grpc.ClientConnInterface) *WorkerClient {
	return &WorkerClient{client: pb.NewAgentWorkerClient(cc)}
}

func (c *WorkerClient) ExecuteTool(ctx context.Context, callID, name, input string) (ipc.ToolResult, error) {
	resp, err := c.client.ExecuteTool(ctx, &pb.ExecuteToolRequest{
		CallId: callID,
		Name:   name,
		Input:  input,
	})
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
