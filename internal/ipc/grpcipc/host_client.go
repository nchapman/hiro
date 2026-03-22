package grpcipc

import (
	"context"
	"fmt"
	"io"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
)

// HostClient implements ipc.AgentHost by making gRPC calls to an AgentHost server.
type HostClient struct {
	client pb.AgentHostClient
}

// NewHostClient creates an AgentHost backed by a gRPC connection.
func NewHostClient(cc grpc.ClientConnInterface) *HostClient {
	return &HostClient{client: pb.NewAgentHostClient(cc)}
}

func (c *HostClient) SpawnAgent(ctx context.Context, agentName, prompt string, onDelta func(string) error) (string, error) {
	stream, err := c.client.SpawnAgent(ctx, &pb.SpawnAgentRequest{
		AgentName: agentName,
		Prompt:    prompt,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onDelta)
}

func (c *HostClient) StartAgent(ctx context.Context, agentName string) (string, error) {
	resp, err := c.client.StartAgent(ctx, &pb.StartAgentRequest{
		AgentName: agentName,
	})
	if err != nil {
		return "", err
	}
	return resp.SessionId, nil
}

func (c *HostClient) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	stream, err := c.client.SendMessage(ctx, &pb.SendMessageRequest{
		AgentId: agentID,
		Message: message,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onDelta)
}

func (c *HostClient) StopAgent(ctx context.Context, agentID string) error {
	_, err := c.client.StopAgent(ctx, &pb.StopAgentRequest{
		AgentId: agentID,
	})
	return err
}

func (c *HostClient) ListAgents(ctx context.Context) ([]ipc.AgentInfo, error) {
	resp, err := c.client.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		return nil, err
	}
	result := make([]ipc.AgentInfo, len(resp.Agents))
	for i, a := range resp.Agents {
		result[i] = ipc.AgentInfo{
			ID:          a.Id,
			Name:        a.Name,
			Mode:        a.Mode,
			Description: a.Description,
			ParentID:    a.ParentId,
		}
	}
	return result, nil
}

func (c *HostClient) GetSecrets(ctx context.Context) (names []string, env []string, err error) {
	resp, err := c.client.GetSecrets(ctx, &pb.GetSecretsRequest{})
	if err != nil {
		return nil, nil, err
	}
	return resp.Names, resp.Env, nil
}

// recvStream reads ChatEvent messages from a server stream, calling onDelta
// for each "delta" event and returning the content of the final "done" event.
func recvStream(stream grpc.ServerStreamingClient[pb.ChatEvent], onDelta func(string) error) (string, error) {
	var result string
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return "", err
		}
		switch event.Type {
		case "delta":
			if onDelta != nil {
				if err := onDelta(event.Content); err != nil {
					return "", err
				}
			}
		case "done":
			result = event.Content
		case "error":
			return "", fmt.Errorf("agent error: %s", event.Content)
		}
	}
}
