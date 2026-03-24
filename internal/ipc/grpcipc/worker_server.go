package grpcipc

import (
	"context"

	"github.com/nchapman/hivebot/internal/ipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WorkerServer adapts an ipc.AgentWorker to the gRPC AgentWorkerServer interface.
type WorkerServer struct {
	pb.UnimplementedAgentWorkerServer
	worker ipc.AgentWorker
}

// NewWorkerServer creates a gRPC server that delegates to the given AgentWorker.
func NewWorkerServer(worker ipc.AgentWorker) *WorkerServer {
	return &WorkerServer{worker: worker}
}

// Register registers this server with a gRPC service registrar.
func (s *WorkerServer) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterAgentWorkerServer(registrar, s)
}

func (s *WorkerServer) Chat(req *pb.ChatRequest, stream grpc.ServerStreamingServer[pb.ChatEvent]) error {
	ctx := stream.Context()
	onEvent := func(evt ipc.ChatEvent) error {
		return stream.Send(chatEventToProto(evt))
	}

	result, err := s.worker.Chat(ctx, req.Message, onEvent)
	if err != nil {
		return status.Errorf(codes.Internal, "chat: %v", err)
	}

	return stream.Send(&pb.ChatEvent{Type: "done", Content: result})
}

func (s *WorkerServer) Shutdown(ctx context.Context, req *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	if err := s.worker.Shutdown(ctx); err != nil {
		return nil, err
	}
	return &pb.ShutdownResponse{}, nil
}
