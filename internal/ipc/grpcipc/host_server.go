// Package grpcipc provides gRPC adapters for the ipc.AgentHost and
// ipc.AgentWorker interfaces.
package grpcipc

import (
	"context"

	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"github.com/nchapman/hivebot/internal/ipc"
	"google.golang.org/grpc"
)

// HostServer adapts an ipc.AgentHost to the gRPC AgentHostServer interface.
type HostServer struct {
	pb.UnimplementedAgentHostServer
	host ipc.AgentHost
}

// NewHostServer creates a gRPC server that delegates to the given AgentHost.
func NewHostServer(host ipc.AgentHost) *HostServer {
	return &HostServer{host: host}
}

// Register registers this server with a gRPC service registrar.
func (s *HostServer) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterAgentHostServer(registrar, s)
}

func (s *HostServer) SpawnAgent(req *pb.SpawnAgentRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	ctx := stream.Context()
	onDelta := func(text string) error {
		return stream.Send(&pb.ChatEvent{Type: "delta", Content: text})
	}

	result, err := s.host.SpawnAgent(ctx, req.AgentName, req.Prompt, onDelta)
	if err != nil {
		return err
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) StartAgent(ctx context.Context, req *pb.StartAgentRequest) (*pb.StartAgentResponse, error) {
	id, err := s.host.StartAgent(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	return &pb.StartAgentResponse{SessionId: id}, nil
}

func (s *HostServer) SendMessage(req *pb.SendMessageRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	ctx := stream.Context()
	onDelta := func(text string) error {
		return stream.Send(&pb.ChatEvent{Type: "delta", Content: text})
	}

	result, err := s.host.SendMessage(ctx, req.AgentId, req.Message, onDelta)
	if err != nil {
		return err
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) StopAgent(ctx context.Context, req *pb.StopAgentRequest) (*pb.StopAgentResponse, error) {
	if err := s.host.StopAgent(ctx, req.AgentId); err != nil {
		return nil, err
	}
	return &pb.StopAgentResponse{}, nil
}

func (s *HostServer) ListAgents(ctx context.Context, req *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	agents, err := s.host.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	pbAgents := make([]*pb.AgentInfoProto, len(agents))
	for i, a := range agents {
		pbAgents[i] = &pb.AgentInfoProto{
			Id:          a.ID,
			Name:        a.Name,
			Mode:        a.Mode,
			Description: a.Description,
			ParentId:    a.ParentID,
		}
	}
	return &pb.ListAgentsResponse{Agents: pbAgents}, nil
}

func (s *HostServer) GetSecrets(ctx context.Context, req *pb.GetSecretsRequest) (*pb.GetSecretsResponse, error) {
	names, env, err := s.host.GetSecrets(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.GetSecretsResponse{Names: names, Env: env}, nil
}
