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
	worker   ipc.AgentWorker
	executor ipc.ToolExecutor // optional; set via SetToolExecutor
}

// NewWorkerServer creates a gRPC server that delegates to the given AgentWorker.
func NewWorkerServer(worker ipc.AgentWorker) *WorkerServer {
	return &WorkerServer{worker: worker}
}

// SetToolExecutor sets the tool executor for ExecuteTool RPCs.
func (s *WorkerServer) SetToolExecutor(executor ipc.ToolExecutor) {
	s.executor = executor
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

func (s *WorkerServer) ConfigChanged(ctx context.Context, req *pb.ConfigChangedRequest) (*pb.ConfigChangedResponse, error) {
	update := ipc.ConfigUpdate{
		Model:       req.Model,
		Provider:    req.Provider,
		APIKey:      req.ApiKey,
		Description: req.Description,
	}
	if req.HasToolRestriction {
		update.EffectiveTools = make(map[string]bool, len(req.EffectiveTools))
		for k, v := range req.EffectiveTools {
			update.EffectiveTools[k] = v
		}
	}
	if err := s.worker.ConfigChanged(ctx, update); err != nil {
		return nil, status.Errorf(codes.Internal, "config changed: %v", err)
	}
	return &pb.ConfigChangedResponse{}, nil
}

func (s *WorkerServer) ExecuteTool(ctx context.Context, req *pb.ExecuteToolRequest) (*pb.ExecuteToolResponse, error) {
	if s.executor == nil {
		return nil, status.Errorf(codes.Unimplemented, "tool execution not available")
	}
	result, err := s.executor.ExecuteTool(ctx, req.CallId, req.Name, req.Input)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "executing tool %s: %v", req.Name, err)
	}
	return &pb.ExecuteToolResponse{
		Content: result.Content,
		IsError: result.IsError,
	}, nil
}
