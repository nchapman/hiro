package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nchapman/hiro/internal/api"
	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"github.com/nchapman/hiro/web"
)

// runWorkerNode starts hiro in worker mode. It connects to the leader,
// receives file sync, and manages local agent worker processes.
func runWorkerNode(rootDir string, cp *controlplane.ControlPlane, logger *slog.Logger) error {
	leaderAddr := cp.ClusterLeaderAddr()
	if envAddr := os.Getenv("HIRO_LEADER"); envAddr != "" {
		leaderAddr = envAddr
	}

	nodeName := cp.ClusterNodeName()
	if nodeName == "" {
		hostname, _ := os.Hostname()
		nodeName = hostname
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load or create node identity for mTLS.
	identity, err := cluster.LoadOrCreateIdentity(rootDir)
	if err != nil {
		return fmt.Errorf("loading node identity: %w", err)
	}
	logger.Info("node identity loaded", "node_id", identity.NodeID[:16]+"...")

	tlsCert, err := cluster.TLSCertFromIdentity(identity)
	if err != nil {
		return fmt.Errorf("generating TLS certificate: %w", err)
	}

	// If tracker is configured, start discovery and use it to find the leader.
	var discovery *cluster.DiscoveryClient
	if trackerURL := cp.ClusterTrackerURL(); trackerURL != "" {
		swarmCode := cp.ClusterSwarmCode()
		if swarmCode == "" {
			return fmt.Errorf("HIRO_SWARM_CODE is required when tracker_url is set")
		}

		discovery = cluster.NewDiscoveryClient(cluster.DiscoveryConfig{
			TrackerURL:     trackerURL,
			SwarmCode:      swarmCode,
			Role:           "worker",
			GRPCPort:       0, // workers don't serve gRPC
			Identity:       identity,
			TLSFingerprint: cluster.TLSFingerprint(tlsCert),
			NodeName:       nodeName,
			Logger:         logger,
		})
		go discovery.Run(ctx)
		logger.Info("tracker discovery started", "tracker", trackerURL, "role", "worker")
	}

	// Resolve leader address: static config takes precedence, then tracker discovery.
	if leaderAddr == "" {
		if discovery == nil {
			return fmt.Errorf("worker mode requires cluster.leader_addr, HIRO_LEADER, or tracker_url + HIRO_SWARM_CODE")
		}
		logger.Info("waiting for leader via tracker discovery...")
		leaderAddr, err = discovery.WaitForLeader(ctx)
		if err != nil {
			return fmt.Errorf("discovering leader: %w", err)
		}
		logger.Info("leader discovered via tracker", "leader", leaderAddr)
	}

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

	// Start a minimal HTTP server so workers have a web UI for settings.
	listenAddr := envOr("HIRO_ADDR", ":8080")
	// Track connection status for the worker status page.
	var connStatus atomic.Value
	connStatus.Store("connecting")

	webFS, _ := web.DistFS()
	httpSrv := api.NewServer(logger, webFS, cp, nil, rootDir)
	httpSrv.SetWorkerStatus(func() string { return connStatus.Load().(string) })
	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           httpSrv,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("worker HTTP server starting", "addr", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("worker HTTP server error", "error", err)
		}
	}()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = httpServer.Shutdown(shutCtx)
	}()

	// Build mTLS config. When using tracker discovery, pin the leader's public
	// key so a MITM can't intercept the connection. For direct leader_addr,
	// the operator already trusts the address — TLS encrypts, identity-based
	// approval handles authentication.
	var leaderPubKey ed25519.PublicKey
	if discovery != nil {
		if leader := discovery.Leader(); leader != nil {
			if raw, err := base64.StdEncoding.DecodeString(leader.PublicKey); err == nil && len(raw) == ed25519.PublicKeySize {
				leaderPubKey = ed25519.PublicKey(raw)
				logger.Info("pinning leader public key from tracker", "node_id", leader.NodeID[:16]+"...")
			}
		}
	}
	clientTLS := cluster.ClientTLSConfig(tlsCert, leaderPubKey)

	// Determine relay URL for fallback connectivity.
	var relayURL string
	if discovery != nil {
		relayURL = discovery.RelayURL()
	}
	swarmCode := cp.ClusterSwarmCode()

	// Build the worker stream with happy eyeballs dialer when relay is available.
	var dialFunc func(ctx context.Context, addr string) (net.Conn, error)
	if relayURL != "" && swarmCode != "" {
		dialFunc = func(ctx context.Context, addr string) (net.Conn, error) {
			return happyEyeballs(ctx, addr, relayURL, swarmCode, identity, logger)
		}
	}

	// Create the worker stream client.
	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: leaderAddr,
		NodeName:   nodeName,
		TLSConfig:  clientTLS,
		DialFunc:   dialFunc,
		Logger:     logger,
	})

	ws.SetOnConnected(func() { connStatus.Store("connected") })

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
	// Uses io.Pipe so the tar is extracted on the fly as chunks arrive,
	// avoiding unbounded memory usage for large workspaces.
	var (
		syncMu     sync.Mutex
		syncPipeW  *io.PipeWriter
		syncDoneCh chan error // signals when extraction goroutine finishes
	)
	ws.SetFileSyncHandler(func(_ context.Context, msg *pb.FileSyncData) {
		syncMu.Lock()
		defer syncMu.Unlock()

		// Lazily create the pipe on the first chunk.
		if syncPipeW == nil {
			pr, pw := io.Pipe()
			syncPipeW = pw
			syncDoneCh = make(chan error, 1)
			go func() {
				syncDoneCh <- syncSvc.ApplyInitialSyncStream(pr)
			}()
		}

		if len(msg.Data) > 0 && syncPipeW != nil {
			if _, err := syncPipeW.Write(msg.Data); err != nil {
				// Extraction failed (e.g. corrupt archive). Close the pipe
				// and abandon this sync — the next reconnect will retry.
				logger.Error("sync pipe broken, aborting initial sync", "error", err)
				syncPipeW.CloseWithError(err)
				syncPipeW = nil
				syncDoneCh = nil
				return
			}
		}

		if msg.Final && syncPipeW != nil {
			syncPipeW.Close()
			if err := <-syncDoneCh; err != nil {
				logger.Error("failed to apply initial sync", "error", err)
			}
			syncPipeW = nil
			syncDoneCh = nil
			logger.Info("initial file sync complete")

			// Start the filesystem watcher AFTER initial sync is fully
			// applied. Starting earlier would cause every extracted file
			// to echo back as a "new change" to the leader.
			go syncSvc.WatchAndSync()
		}
	})
	ws.SetFileUpdateHandler(func(_ context.Context, msg *pb.FileUpdate) {
		if err := syncSvc.ApplyFileUpdate(msg); err != nil {
			logger.Warn("failed to apply file update", "path", msg.Path, "error", err)
		}
	})

	// Use a closure so the defer always stops the current sync service,
	// even after reconnect replaces it with a fresh instance.
	defer func() { syncSvc.Stop() }()

	// Connect to leader with reconnection.
	for {
		connStatus.Store("connecting")
		err := ws.Connect(ctx)
		connStatus.Store("disconnected")
		// Clean up all local workers from the previous connection
		// before retrying, to prevent goroutine/resource leaks.
		bridge.ShutdownAll(context.Background())

		// Stop the file watcher and reset sync pipe state for clean reconnect.
		// The watcher will be restarted after the next initial sync completes.
		syncSvc.Stop()
		syncMu.Lock()
		if syncPipeW != nil {
			syncPipeW.CloseWithError(fmt.Errorf("disconnected"))
			syncPipeW = nil
			syncDoneCh = nil
		}
		syncMu.Unlock()

		// Create a fresh sync service for the new connection (Stop is one-shot).
		syncSvc = cluster.NewFileSyncService(cluster.FileSyncConfig{
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

		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, cluster.ErrPendingApproval) {
			connStatus.Store("pending")
			logger.Info("awaiting approval from leader, will retry...")
		} else {
			logger.Warn("disconnected from leader, reconnecting...", "error", err)
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}

		// Re-resolve leader address from tracker if available.
		if discovery != nil {
			discovery.Announce(ctx) // fresh data before checking
			if newAddr := discovery.LeaderAddr(); newAddr != "" && newAddr != ws.LeaderAddr() {
				logger.Info("leader address changed via tracker", "old", ws.LeaderAddr(), "new", newAddr)
				ws.SetLeaderAddr(newAddr)
			}
		}
	}

	logger.Info("worker node shutting down")
	return nil
}

// happyEyeballs tries a direct TCP connection first, then falls back to
// the relay after a short delay. First successful connection wins.
func happyEyeballs(ctx context.Context, directAddr, relayAddr, swarmCode string, identity *cluster.NodeIdentity, logger *slog.Logger) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
		via  string
	}
	ch := make(chan result, 2)

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	defer dialCancel()

	// Direct attempt — starts immediately.
	go func() {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		conn, err := dialer.DialContext(dialCtx, "tcp", directAddr)
		ch <- result{conn, err, "direct"}
	}()

	// Relay attempt — starts after 500ms delay to prefer direct.
	go func() {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-dialCtx.Done():
			ch <- result{nil, dialCtx.Err(), "relay"}
			return
		}
		conn, err := cluster.DialRelay(dialCtx, relayAddr, swarmCode, identity)
		ch <- result{conn, err, "relay"}
	}()

	// Take first success.
	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err == nil {
			logger.Info("connected to leader", "via", r.via)
			dialCancel()
			// Drain and close any second successful connection.
			go func() {
				if r2 := <-ch; r2.err == nil {
					r2.conn.Close()
				}
			}()
			return r.conn, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", r.via, r.err)
		}
	}
	return nil, fmt.Errorf("all connection attempts failed: %w", firstErr)
}
