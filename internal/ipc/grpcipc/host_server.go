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

// SpawnSession creates a child of the caller. No descendant check needed because
// the caller IS the parent — parent_id is the caller's own ID, injected by HostClient.
func (s *HostServer) SpawnSession(req *pb.SpawnSessionRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	ctx := stream.Context()
	onEvent := func(evt ipc.ChatEvent) error {
		return stream.Send(chatEventToProto(evt))
	}

	result, err := s.mgr.SpawnSession(ctx, req.AgentName, req.Prompt, req.ParentId, onEvent)
	if err != nil {
		return status.Errorf(codes.Internal, "spawn session: %v", err)
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) CreateSession(ctx context.Context, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	id, err := s.mgr.CreateSession(ctx, req.AgentName, req.ParentId, req.Mode)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	return &pb.CreateSessionResponse{SessionId: id}, nil
}

func (s *HostServer) SendMessage(req *pb.SendMessageRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	if err := s.checkDescendant(req.SessionId, req.CallerId); err != nil {
		return err
	}

	ctx := stream.Context()
	onEvent := func(evt ipc.ChatEvent) error {
		return stream.Send(chatEventToProto(evt))
	}

	result, err := s.mgr.SendMessage(ctx, req.SessionId, req.Message, onEvent)
	if err != nil {
		return status.Errorf(codes.Internal, "send message: %v", err)
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *HostServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	if err := s.checkDescendant(req.SessionId, req.CallerId); err != nil {
		return nil, err
	}

	if _, err := s.mgr.StopSession(req.SessionId); err != nil {
		return nil, err
	}
	return &pb.StopSessionResponse{}, nil
}

func (s *HostServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	if err := s.checkDescendant(req.SessionId, req.CallerId); err != nil {
		return nil, err
	}

	if err := s.mgr.StartSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	return &pb.StartSessionResponse{}, nil
}

func (s *HostServer) DeleteSession(ctx context.Context, req *pb.DeleteSessionRequest) (*pb.DeleteSessionResponse, error) {
	if err := s.checkDescendant(req.SessionId, req.CallerId); err != nil {
		return nil, err
	}

	if err := s.mgr.DeleteSession(req.SessionId); err != nil {
		return nil, err
	}
	return &pb.DeleteSessionResponse{}, nil
}

func (s *HostServer) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	sessions := s.mgr.ListChildSessions(req.ParentId)
	pbSessions := make([]*pb.SessionInfoProto, len(sessions))
	for i, si := range sessions {
		pbSessions[i] = &pb.SessionInfoProto{
			Id:          si.ID,
			Name:        si.Name,
			Mode:        si.Mode,
			Description: si.Description,
			ParentId:    si.ParentID,
			Status:      si.Status,
		}
	}
	return &pb.ListSessionsResponse{Sessions: pbSessions}, nil
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
			fmt.Sprintf("session %q is not a descendant of caller %q", targetID, callerID))
	}
	return nil
}
