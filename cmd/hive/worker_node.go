package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/controlplane"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
)

// runWorkerNode starts hive in worker mode. It connects to the leader,
// receives file sync, and manages local agent worker processes.
func runWorkerNode(rootDir string, cp *controlplane.ControlPlane, logger *slog.Logger) error {
	leaderAddr := cp.ClusterLeaderAddr()
	if envAddr := os.Getenv("HIVE_LEADER"); envAddr != "" {
		leaderAddr = envAddr
	}
	if leaderAddr == "" {
		return fmt.Errorf("worker mode requires cluster.leader_addr in config.yaml or HIVE_LEADER env var")
	}

	joinToken := cp.ClusterJoinToken()
	if envToken := os.Getenv("HIVE_JOIN_TOKEN"); envToken != "" {
		joinToken = envToken
	}
	if joinToken == "" {
		return fmt.Errorf("worker mode requires cluster.join_token in config.yaml or HIVE_JOIN_TOKEN env var")
	}

	nodeName := cp.ClusterNodeName()
	if nodeName == "" {
		hostname, _ := os.Hostname()
		nodeName = hostname
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create workspace directories locally.
	for _, dir := range []string{"workspace", "instances"} {
		if err := os.MkdirAll(fmt.Sprintf("%s/%s", rootDir, dir), 0755); err != nil {
			return fmt.Errorf("creating %s directory: %w", dir, err)
		}
	}

	logger.Info("starting in worker mode",
		"leader", leaderAddr,
		"node_name", nodeName,
	)

	// Create the worker stream client.
	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: leaderAddr,
		NodeName:   nodeName,
		JoinToken:  joinToken,
		Logger:     logger,
	})

	// Create the node bridge that manages local workers.
	bridge := cluster.NewNodeBridge(rootDir, ws, logger)

	// Create file sync service for receiving updates from leader.
	syncSvc := cluster.NewFileSyncService(cluster.FileSyncConfig{
		RootDir:  rootDir,
		SyncDirs: []string{"agents", "skills", "workspace"},
		NodeID:   nodeName,
		SendFn: func(update *pb.FileUpdate) error {
			return ws.Send(&pb.NodeMessage{
				Msg: &pb.NodeMessage_FileUpdate{FileUpdate: update},
			})
		},
		Logger: logger,
	})

	// Wire up file sync handlers.
	var initialSyncBuf []byte
	ws.SetFileSyncHandler(func(_ context.Context, msg *pb.FileSyncData) {
		initialSyncBuf = append(initialSyncBuf, msg.Data...)
		if msg.Final {
			if err := syncSvc.ApplyInitialSync(initialSyncBuf); err != nil {
				logger.Error("failed to apply initial sync", "error", err)
			}
			initialSyncBuf = nil
			logger.Info("initial file sync complete")
		}
	})
	ws.SetFileUpdateHandler(func(_ context.Context, msg *pb.FileUpdate) {
		if err := syncSvc.ApplyFileUpdate(msg); err != nil {
			logger.Warn("failed to apply file update", "path", msg.Path, "error", err)
		}
	})

	// Start file watcher for sending local changes back to leader.
	go syncSvc.WatchAndSync()
	defer syncSvc.Stop()

	// Connect to leader with reconnection.
	for {
		err := ws.Connect(ctx)
		// Clean up all local workers from the previous connection
		// before retrying, to prevent goroutine/resource leaks.
		bridge.ShutdownAll(context.Background())
		// Reset initial sync buffer for clean reconnect.
		initialSyncBuf = nil

		if ctx.Err() != nil {
			break
		}
		logger.Warn("disconnected from leader, reconnecting...", "error", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}

	logger.Info("worker node shutting down")
	return nil
}
