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
	worker      ipc.AgentWorker
	onSecretEnv func([]string) // called when secret env vars arrive with a tool call
}

// NewWorkerServer creates a gRPC server that delegates to the given AgentWorker.
func NewWorkerServer(worker ipc.AgentWorker) *WorkerServer {
	return &WorkerServer{worker: worker}
}

// SetSecretEnvCallback sets a callback invoked when secret env vars arrive
// with a tool call. The worker uses this to inject secrets into bash commands.
func (s *WorkerServer) SetSecretEnvCallback(fn func([]string)) {
	s.onSecretEnv = fn
}

// Register registers this server with a gRPC service registrar.
func (s *WorkerServer) Register(registrar grpc.ServiceRegistrar) {
	pb.RegisterAgentWorkerServer(registrar, s)
}

func (s *WorkerServer) ExecuteTool(ctx context.Context, req *pb.ExecuteToolRequest) (*pb.ExecuteToolResponse, error) {
	if s.worker == nil {
		return nil, status.Errorf(codes.Unimplemented, "tool execution not available")
	}
	// Inject secret env vars into the worker's context for bash commands.
	if len(req.SecretEnv) > 0 && s.onSecretEnv != nil {
		s.onSecretEnv(req.SecretEnv)
	}
	result, err := s.worker.ExecuteTool(ctx, req.CallId, req.Name, req.Input)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "executing tool %s: %v", req.Name, err)
	}
	return &pb.ExecuteToolResponse{
		Content: result.Content,
		IsError: result.IsError,
	}, nil
}

func (s *WorkerServer) Shutdown(ctx context.Context, req *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	if err := s.worker.Shutdown(ctx); err != nil {
		return nil, err
	}
	return &pb.ShutdownResponse{}, nil
}
