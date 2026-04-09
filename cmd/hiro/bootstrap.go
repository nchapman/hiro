package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

const (
	// grpcMaxConcurrentStreams limits per-connection gRPC streams.
	grpcMaxConcurrentStreams = 64

	// keepalivePingInterval is how often the gRPC server pings idle connections.
	keepalivePingInterval = 30 * time.Second

	// keepalivePingTimeout is how long to wait for a keepalive ping response.
	keepalivePingTimeout = 10 * time.Second

	// keepaliveMinClientInterval is the minimum allowed ping interval from clients.
	keepaliveMinClientInterval = 10 * time.Second
)

// clusterState bundles all cluster infrastructure created at startup.
type clusterState struct {
	grpcServer   *grpc.Server
	service      *cluster.LeaderService
	leaderStream *cluster.LeaderStream
	fileSync     *cluster.FileSyncService
	listener     net.Listener
	registry     *cluster.NodeRegistry
	pending      *cluster.PendingRegistry
}

// setupNodeIdentity loads or creates the node's Ed25519 keypair and derives
// a TLS certificate for mTLS.
func setupNodeIdentity(rootDir string, logger *slog.Logger) (*cluster.NodeIdentity, tls.Certificate, error) {
	identity, err := cluster.LoadOrCreateIdentity(rootDir)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("loading node identity: %w", err)
	}
	logger.Info("node identity loaded", "node_id", identity.NodeID[:16]+"...")

	tlsCert, err := cluster.TLSCertFromIdentity(identity)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("generating TLS certificate: %w", err)
	}
	logger.Info("cluster mTLS enabled", "fingerprint", cluster.TLSFingerprint(tlsCert)[:16]+"...")

	return identity, tlsCert, nil
}

// setupClusterServer creates and starts the gRPC cluster server that accepts
// worker node connections, and the file sync service for pushing workspace
// changes to workers.
func setupClusterServer(rootDir string, tlsCert tls.Certificate, cp *controlplane.ControlPlane, logger *slog.Logger) (clusterState, error) {
	clusterAddr := envOr("HIRO_CLUSTER_ADDR", ":8121")

	registry := cluster.NewNodeRegistry()
	homeName := cp.ClusterNodeName()
	if homeName == "" {
		homeName = envOr("HOSTNAME", "leader")
	}
	registry.RegisterHome(homeName)

	pending := cluster.NewPendingRegistry(filepath.Join(rootDir, "config", "pending_nodes.yaml"), logger)
	if err := pending.Load(); err != nil {
		logger.Warn("failed to load pending nodes", "error", err)
	}

	// Bridge control plane node approval into cluster approval status.
	leaderStream := cluster.NewLeaderStream(registry, func(nodeID string) cluster.ApprovalStatus {
		switch cp.NodeApprovalCheck(nodeID) {
		case controlplane.NodeStatusApproved:
			return cluster.ApprovalGranted
		case controlplane.NodeStatusRevoked:
			return cluster.ApprovalRevoked
		default:
			return cluster.ApprovalPending
		}
	}, pending, logger)

	grpcSrv := newClusterGRPCServer(tlsCert)
	leaderStream.Register(grpcSrv)

	lis, err := net.Listen("tcp", clusterAddr) //nolint:noctx // startup-time listener
	if err != nil {
		return clusterState{}, fmt.Errorf("listening on cluster addr %s: %w", clusterAddr, err)
	}

	svc := cluster.NewLeaderService(leaderStream, registry, logger)
	fileSync := startLeaderFileSync(rootDir, svc, logger)

	go func() {
		logger.Info("cluster gRPC server starting", "addr", clusterAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error("cluster gRPC error", "error", err)
		}
	}()

	return clusterState{
		grpcServer:   grpcSrv,
		service:      svc,
		leaderStream: leaderStream,
		fileSync:     fileSync,
		listener:     lis,
		registry:     registry,
		pending:      pending,
	}, nil
}

// startLeaderFileSync creates and starts the file sync service that pushes
// workspace/agents/skills changes to connected worker nodes.
func startLeaderFileSync(rootDir string, svc *cluster.LeaderService, logger *slog.Logger) *cluster.FileSyncService {
	fileSync := cluster.NewFileSyncService(cluster.FileSyncConfig{
		RootDir:  rootDir,
		SyncDirs: []string{"agents", "skills", "workspace"},
		NodeID:   "leader",
		SendFn: func(update *pb.FileUpdate) error {
			svc.BroadcastFileUpdate(update)
			return nil
		},
		Logger: logger,
	})
	svc.SetFileSync(fileSync)
	go func() {
		if err := fileSync.WatchAndSync(); err != nil {
			logger.Warn("file sync watcher stopped", "error", err)
		}
	}()
	return fileSync
}

// newClusterGRPCServer creates a gRPC server configured with mTLS and keepalive.
func newClusterGRPCServer(tlsCert tls.Certificate) *grpc.Server {
	serverTLS := cluster.ServerTLSConfig(tlsCert)
	return grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    keepalivePingInterval,
			Timeout: keepalivePingTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinClientInterval,
			PermitWithoutStream: true, // allow pings even with no active RPCs
		}),
	)
}

// bootstrapOperator ensures the operator agent is running. It handles
// three cases: already running (no-op), stopped (restart), or missing (create).
// Returns the leader instance ID (empty if no operator is defined).
func bootstrapOperator(ctx context.Context, mgr *agent.Manager, logger *slog.Logger) (string, error) {
	leaderID, alreadyRunning := mgr.InstanceByAgentName("operator")
	if alreadyRunning {
		return leaderID, nil
	}

	if leaderID != "" {
		// Stopped operator found — restart it.
		if err := mgr.StartInstance(ctx, leaderID); err != nil {
			if os.IsNotExist(err) {
				logger.Warn("operator agent definition missing, skipping restart")
				return "", nil
			}
			return "", fmt.Errorf("restarting operator: %w", err)
		}
		return leaderID, nil
	}

	// No operator at all — create one.
	leaderID, err := mgr.CreateInstance(ctx, "operator", "", "persistent", "", "Hiro", "", "")
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("no operator agent defined, skipping")
			return "", nil
		}
		return "", fmt.Errorf("starting leader agent: %w", err)
	}
	return leaderID, nil
}
