package grpcipc

import (
	"context"
	"io"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
)

// HostClient implements ipc.AgentHost by making gRPC calls to an AgentHost server.
// It transparently injects the caller's identity into every request so the server
// can scope authorization without the caller having to manage parent IDs.
type HostClient struct {
	client   pb.AgentHostClient
	callerID string
}

// NewHostClient creates an AgentHost backed by a gRPC connection.
// callerID is this agent's session ID — it is injected as parent_id or
// caller_id in every request for authorization scoping.
func NewHostClient(cc grpc.ClientConnInterface, callerID string) *HostClient {
	return &HostClient{
		client:   pb.NewAgentHostClient(cc),
		callerID: callerID,
	}
}

func (c *HostClient) SpawnAgent(ctx context.Context, agentName, prompt string, onDelta func(string) error) (string, error) {
	stream, err := c.client.SpawnAgent(ctx, &pb.SpawnAgentRequest{
		AgentName: agentName,
		Prompt:    prompt,
		ParentId:  c.callerID,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onDelta)
}

func (c *HostClient) StartAgent(ctx context.Context, agentName string) (string, error) {
	resp, err := c.client.StartAgent(ctx, &pb.StartAgentRequest{
		AgentName: agentName,
		ParentId:  c.callerID,
	})
	if err != nil {
		return "", err
	}
	return resp.SessionId, nil
}

func (c *HostClient) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	stream, err := c.client.SendMessage(ctx, &pb.SendMessageRequest{
		AgentId:  agentID,
		Message:  message,
		CallerId: c.callerID,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onDelta)
}

func (c *HostClient) StopAgent(ctx context.Context, agentID string) error {
	_, err := c.client.StopAgent(ctx, &pb.StopAgentRequest{
		AgentId:  agentID,
		CallerId: c.callerID,
	})
	return err
}

func (c *HostClient) ListAgents(ctx context.Context) ([]ipc.AgentInfo, error) {
	resp, err := c.client.ListAgents(ctx, &pb.ListAgentsRequest{
		ParentId: c.callerID,
	})
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
			return event.Content, nil
		}
	}
}
