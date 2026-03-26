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

func (c *WorkerClient) Chat(ctx context.Context, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	stream, err := c.client.Chat(ctx, &pb.ChatRequest{Message: message})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onEvent)
}

func (c *WorkerClient) Shutdown(ctx context.Context) error {
	_, err := c.client.Shutdown(ctx, &pb.ShutdownRequest{})
	return err
}

func (c *WorkerClient) ConfigChanged(ctx context.Context, update ipc.ConfigUpdate) error {
	req := &pb.ConfigChangedRequest{
		Model:       update.Model,
		Provider:    update.Provider,
		ApiKey:      update.APIKey,
		Description: update.Description,
	}
	if update.EffectiveTools != nil {
		req.HasToolRestriction = true
		req.EffectiveTools = make(map[string]bool, len(update.EffectiveTools))
		for k, v := range update.EffectiveTools {
			req.EffectiveTools[k] = v
		}
	}
	_, err := c.client.ConfigChanged(ctx, req)
	return err
}
