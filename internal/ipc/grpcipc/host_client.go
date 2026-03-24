package grpcipc

import (
	"context"
	"encoding/json"
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

func (c *HostClient) SpawnSession(ctx context.Context, agentName, prompt string, onEvent func(ipc.ChatEvent) error) (string, error) {
	stream, err := c.client.SpawnSession(ctx, &pb.SpawnSessionRequest{
		AgentName: agentName,
		Prompt:    prompt,
		ParentId:  c.callerID,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onEvent)
}

func (c *HostClient) CreateSession(ctx context.Context, agentName string) (string, error) {
	resp, err := c.client.CreateSession(ctx, &pb.CreateSessionRequest{
		AgentName: agentName,
		ParentId:  c.callerID,
	})
	if err != nil {
		return "", err
	}
	return resp.SessionId, nil
}

func (c *HostClient) SendMessage(ctx context.Context, sessionID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	stream, err := c.client.SendMessage(ctx, &pb.SendMessageRequest{
		SessionId: sessionID,
		Message:   message,
		CallerId:  c.callerID,
	})
	if err != nil {
		return "", err
	}
	return recvStream(stream, onEvent)
}

func (c *HostClient) StopSession(ctx context.Context, sessionID string) error {
	_, err := c.client.StopSession(ctx, &pb.StopSessionRequest{
		SessionId: sessionID,
		CallerId:  c.callerID,
	})
	return err
}

func (c *HostClient) StartSession(ctx context.Context, sessionID string) error {
	_, err := c.client.StartSession(ctx, &pb.StartSessionRequest{
		SessionId: sessionID,
		CallerId:  c.callerID,
	})
	return err
}

func (c *HostClient) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := c.client.DeleteSession(ctx, &pb.DeleteSessionRequest{
		SessionId: sessionID,
		CallerId:  c.callerID,
	})
	return err
}

func (c *HostClient) ListSessions(ctx context.Context) ([]ipc.SessionInfo, error) {
	resp, err := c.client.ListSessions(ctx, &pb.ListSessionsRequest{
		ParentId: c.callerID,
	})
	if err != nil {
		return nil, err
	}
	result := make([]ipc.SessionInfo, len(resp.Sessions))
	for i, s := range resp.Sessions {
		result[i] = ipc.SessionInfo{
			ID:          s.Id,
			Name:        s.Name,
			Mode:        s.Mode,
			Description: s.Description,
			ParentID:    s.ParentId,
			Status:      s.Status,
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

// recvStream reads ChatEvent messages from a server stream, calling onEvent
// for each event and returning the content of the final "done" event.
func recvStream(stream grpc.ServerStreamingClient[pb.ChatEvent], onEvent func(ipc.ChatEvent) error) (string, error) {
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if event.Type == "done" {
			return event.Content, nil
		}
		if onEvent != nil {
			evt := protoToChatEvent(event)
			if err := onEvent(evt); err != nil {
				return "", err
			}
		}
	}
}

// chatEventToProto converts an ipc.ChatEvent to a proto ChatEvent.
// Tool call/result data is JSON-encoded in the content field.
func chatEventToProto(evt ipc.ChatEvent) *pb.ChatEvent {
	switch evt.Type {
	case "tool_call":
		data, _ := json.Marshal(map[string]string{
			"tool_call_id": evt.ToolCallID,
			"tool_name":    evt.ToolName,
			"input":        evt.Input,
			"status":       evt.Status,
		})
		return &pb.ChatEvent{Type: "tool_call", Content: string(data)}
	case "tool_result":
		data, _ := json.Marshal(map[string]any{
			"tool_call_id": evt.ToolCallID,
			"output":       evt.Output,
			"is_error":     evt.IsError,
		})
		return &pb.ChatEvent{Type: "tool_result", Content: string(data)}
	default:
		return &pb.ChatEvent{Type: evt.Type, Content: evt.Content}
	}
}

// protoToChatEvent converts a proto ChatEvent to an ipc.ChatEvent.
func protoToChatEvent(event *pb.ChatEvent) ipc.ChatEvent {
	switch event.Type {
	case "tool_call":
		var data struct {
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Input      string `json:"input"`
			Status     string `json:"status"`
		}
		json.Unmarshal([]byte(event.Content), &data)
		return ipc.ChatEvent{
			Type:       "tool_call",
			ToolCallID: data.ToolCallID,
			ToolName:   data.ToolName,
			Input:      data.Input,
			Status:     data.Status,
		}
	case "tool_result":
		var data struct {
			ToolCallID string `json:"tool_call_id"`
			Output     string `json:"output"`
			IsError    bool   `json:"is_error"`
		}
		json.Unmarshal([]byte(event.Content), &data)
		return ipc.ChatEvent{
			Type:       "tool_result",
			ToolCallID: data.ToolCallID,
			Output:     data.Output,
			IsError:    data.IsError,
		}
	default:
		return ipc.ChatEvent{Type: event.Type, Content: event.Content}
	}
}
