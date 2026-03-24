// Package grpcipc provides gRPC adapters for the ipc.AgentHost and
// ipc.AgentWorker interfaces.
package grpcipc

import (
	"context"
	"fmt"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HostServer adapts an ipc.HostManager to the gRPC AgentHostServer interface.
// It reads parent_id / caller_id from each request to scope operations,
// allowing a single server to multiplex requests from all agent processes.
type HostServer struct {
	pb.UnimplementedAgentHostServer
	mgr ipc.HostManager
}

// NewHostServer creates a gRPC server that delegates to the given HostManager.
func NewHostServer(mgr ipc.HostManager) *HostServer {
	return &HostServer{mgr: mgr}
}

// Register registers this server with a gRPC service registrar.
func (s *HostServer) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterAgentHostServer(registrar, s)
}

// SpawnAgent creates a child of the caller. No descendant check needed because
// the caller IS the parent — parent_id is the caller's own ID, injected by HostClient.
func (s *HostServer) SpawnAgent(req *pb.SpawnAgentRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	ctx := stream.Context()
	onEvent := func(evt ipc.ChatEvent) error {
		return stream.Send(chatEventToProto(evt))
	}

	result, err := s.mgr.SpawnSubagent(ctx, req.AgentName, req.Prompt, req.ParentId, onEvent)
	if err != nil {
		return status.Errorf(codes.Internal, "spawn agent: %v", err)
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) StartAgent(ctx context.Context, req *pb.StartAgentRequest) (*pb.StartAgentResponse, error) {
	id, err := s.mgr.StartAgent(ctx, req.AgentName, req.ParentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "start agent: %v", err)
	}
	return &pb.StartAgentResponse{SessionId: id}, nil
}

func (s *HostServer) SendMessage(req *pb.SendMessageRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	if err := s.checkDescendant(req.AgentId, req.CallerId); err != nil {
		return err
	}

	ctx := stream.Context()
	onEvent := func(evt ipc.ChatEvent) error {
		return stream.Send(chatEventToProto(evt))
	}

	result, err := s.mgr.SendMessage(ctx, req.AgentId, req.Message, onEvent)
	if err != nil {
		return status.Errorf(codes.Internal, "send message: %v", err)
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) StopAgent(ctx context.Context, req *pb.StopAgentRequest) (*pb.StopAgentResponse, error) {
	if err := s.checkDescendant(req.AgentId, req.CallerId); err != nil {
		return nil, err
	}

	if _, err := s.mgr.StopAgent(req.AgentId); err != nil {
		return nil, err
	}
	return &pb.StopAgentResponse{}, nil
}

func (s *HostServer) ListAgents(ctx context.Context, req *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	agents := s.mgr.ListChildren(req.ParentId)
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
	return &pb.GetSecretsResponse{
		Names: s.mgr.SecretNames(),
		Env:   s.mgr.SecretEnv(),
	}, nil
}

// checkDescendant verifies that targetID is a descendant of callerID.
func (s *HostServer) checkDescendant(targetID, callerID string) error {
	if callerID == "" {
		return status.Error(codes.InvalidArgument, "caller_id is required")
	}
	if !s.mgr.IsDescendant(targetID, callerID) {
		return status.Error(codes.PermissionDenied,
			fmt.Sprintf("agent %q is not a descendant of caller %q", targetID, callerID))
	}
	return nil
}
