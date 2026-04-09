package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
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
	"github.com/nchapman/hiro/internal/platform/fsperm"
	"github.com/nchapman/hiro/web"
)

const (
	// workerReconnectDelay is how long to wait before reconnecting to the leader.
	workerReconnectDelay = 5 * time.Second

	// happyEyeballsTimeout is the overall timeout for dual-path connection attempts.
	happyEyeballsTimeout = 15 * time.Second

	// happyEyeballsDialTimeout is the timeout for the direct TCP dial.
	happyEyeballsDialTimeout = 10 * time.Second

	// happyEyeballsAttempts is the number of connection paths (direct + relay).
	happyEyeballsAttempts = 2

	// happyEyeballsRelayDelay is how long to wait before starting the relay attempt,
	// giving the direct connection a head start.
	happyEyeballsRelayDelay = 500 * time.Millisecond

	// workerShutdownTimeout is the grace period for the worker's HTTP server shutdown.
	// Intentionally shorter than the main server's shutdownTimeout (10s).
	workerShutdownTimeout = 5 * time.Second
)

// workerNode holds state for a hiro worker that connects to a leader.
type workerNode struct {
	rootDir  string
	cp       *controlplane.ControlPlane
	logger   *slog.Logger
	nodeName string

	ctx    context.Context
	cancel context.CancelFunc

	identity  *cluster.NodeIdentity
	tlsCert   tls.Certificate
	discovery *cluster.DiscoveryClient

	ws      *cluster.WorkerStream
	bridge  *cluster.NodeBridge
	syncSvc *cluster.FileSyncService

	connStatus       atomic.Value
	restartRequested atomic.Bool

	// File sync pipe state, protected by syncMu.
	syncMu     sync.Mutex
	syncPipeW  *io.PipeWriter
	syncDoneCh chan error
}

// runWorkerNode starts hiro in worker mode. It connects to the leader,
// receives file sync, and manages local agent worker processes.
func runWorkerNode(rootDir string, cp *controlplane.ControlPlane, logger *slog.Logger) error {
	wn := &workerNode{
		rootDir: rootDir,
		cp:      cp,
		logger:  logger,
	}
	wn.connStatus.Store("connecting")

	if err := wn.init(); err != nil {
		return err
	}

	wn.startHTTPServer()

	leaderAddr, err := wn.resolveLeaderAddr()
	if err != nil {
		return err
	}

	wn.logger.Info("starting in worker mode",
		"leader", leaderAddr,
		"node_name", wn.nodeName,
	)

	termMgr := wn.initStream(leaderAddr)
	defer termMgr.Shutdown()

	return wn.connectLoop()
}

// init sets up signal handling, node identity, discovery, and workspace dirs.
func (wn *workerNode) init() error {
	wn.nodeName = wn.cp.ClusterNodeName()
	if wn.nodeName == "" {
		hostname, _ := os.Hostname()
		wn.nodeName = hostname
	}

	wn.ctx, wn.cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	identity, err := cluster.LoadOrCreateIdentity(wn.rootDir)
	if err != nil {
		return fmt.Errorf("loading node identity: %w", err)
	}
	wn.identity = identity
	wn.logger.Info("node identity loaded", "node_id", identity.NodeID[:16]+"...")

	tlsCert, err := cluster.TLSCertFromIdentity(identity)
	if err != nil {
		return fmt.Errorf("generating TLS certificate: %w", err)
	}
	wn.tlsCert = tlsCert

	// Start tracker discovery if configured.
	if trackerURL := wn.cp.ClusterTrackerURL(); trackerURL != "" {
		swarmCode := wn.cp.ClusterSwarmCode()
		if swarmCode == "" {
			return fmt.Errorf("HIRO_SWARM_CODE is required when tracker_url is set")
		}

		wn.discovery = cluster.NewDiscoveryClient(cluster.DiscoveryConfig{
			TrackerURL:     trackerURL,
			SwarmCode:      swarmCode,
			Role:           "worker",
			GRPCPort:       0, // workers don't serve gRPC
			Identity:       identity,
			TLSFingerprint: cluster.TLSFingerprint(tlsCert),
			NodeName:       wn.nodeName,
			Logger:         wn.logger,
		})
		go wn.discovery.Run(wn.ctx)
		wn.logger.Info("tracker discovery started", "tracker", trackerURL, "role", "worker")
	}

	// Create workspace directories locally.
	for _, dir := range []string{"workspace", "instances"} {
		if err := os.MkdirAll(fmt.Sprintf("%s/%s", wn.rootDir, dir), fsperm.DirStandard); err != nil {
			return fmt.Errorf("creating %s directory: %w", dir, err)
		}
	}

	return nil
}

// startHTTPServer runs a minimal HTTP server for the worker's web UI / settings.
func (wn *workerNode) startHTTPServer() {
	listenAddr := envOr("HIRO_ADDR", ":8120")

	webFS, err := web.DistFS()
	if err != nil {
		wn.logger.Warn("failed to load web UI assets", "error", err)
	}
	httpSrv := api.NewServer(wn.logger, webFS, wn.cp, nil, wn.rootDir)
	httpSrv.SetWorkerStatus(func() string {
		s, _ := wn.connStatus.Load().(string)
		return s
	})
	httpSrv.SetRestartFunc(func() {
		wn.restartRequested.Store(true)
		wn.cancel() // unblock the reconnect loop
	})

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           httpSrv,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
	go func() {
		wn.logger.Info("worker HTTP server starting", "addr", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			wn.logger.Error("worker HTTP server error", "error", err)
		}
	}()

	// Register shutdown in a goroutine that waits for context cancellation,
	// since we can't use defer in a method that doesn't own the lifecycle.
	go func() {
		<-wn.ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), workerShutdownTimeout)
		defer shutCancel()
		_ = httpServer.Shutdown(shutCtx)
	}()
}

// resolveLeaderAddr determines the leader address from config or tracker discovery.
func (wn *workerNode) resolveLeaderAddr() (string, error) {
	leaderAddr := wn.cp.ClusterLeaderAddr()
	if envAddr := os.Getenv("HIRO_LEADER"); envAddr != "" {
		leaderAddr = envAddr
	}

	if leaderAddr == "" {
		if wn.discovery == nil {
			return "", fmt.Errorf("worker mode requires cluster.leader_addr, HIRO_LEADER, or tracker_url + HIRO_SWARM_CODE")
		}
		wn.logger.Info("waiting for leader via tracker discovery...")
		var err error
		leaderAddr, err = wn.discovery.WaitForLeader(wn.ctx)
		if err != nil {
			return "", fmt.Errorf("discovering leader: %w", err)
		}
		wn.logger.Info("leader discovered via tracker", "leader", leaderAddr)
	}

	return leaderAddr, nil
}

// initStream creates the worker stream client, node bridge, file sync service,
// and wires up all the handlers. Returns the terminal manager so the caller
// can defer its synchronous shutdown (ensuring PTYs are cleaned up on exit).
func (wn *workerNode) initStream(leaderAddr string) *cluster.WorkerTerminalManager {
	// Build mTLS config. When using tracker discovery, pin the leader's public
	// key so a MITM can't intercept the connection. For direct leader_addr,
	// the operator already trusts the address — TLS encrypts, identity-based
	// approval handles authentication.
	var leaderPubKey ed25519.PublicKey
	if wn.discovery != nil {
		if leader := wn.discovery.Leader(); leader != nil {
			if raw, err := base64.StdEncoding.DecodeString(leader.PublicKey); err == nil && len(raw) == ed25519.PublicKeySize {
				leaderPubKey = ed25519.PublicKey(raw)
				wn.logger.Info("pinning leader public key from tracker", "node_id", leader.NodeID[:16]+"...")
			}
		}
	}
	clientTLS := cluster.ClientTLSConfig(wn.tlsCert, leaderPubKey)

	// Build happy eyeballs dialer when relay is available.
	var dialFunc func(ctx context.Context, addr string) (net.Conn, error)
	swarmCode := wn.cp.ClusterSwarmCode()
	if relayURL := wn.discoveryRelayURL(); relayURL != "" && swarmCode != "" {
		dialFunc = func(ctx context.Context, addr string) (net.Conn, error) {
			return happyEyeballs(ctx, addr, relayURL, swarmCode, wn.identity, wn.logger)
		}
	}

	wn.ws = cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: leaderAddr,
		NodeName:   wn.nodeName,
		TLSConfig:  clientTLS,
		DialFunc:   dialFunc,
		Logger:     wn.logger,
	})
	wn.ws.SetOnConnected(func() { wn.connStatus.Store("connected") })

	wn.bridge = cluster.NewNodeBridge(wn.rootDir, wn.ws, wn.logger)

	wn.syncSvc = wn.newFileSyncService()
	wn.wireFileSyncHandlers()

	return cluster.NewWorkerTerminalManager(wn.ws, wn.rootDir, wn.logger)
}

// connectLoop runs the reconnect loop, handling disconnects and re-resolving
// the leader address from tracker discovery when available.
func (wn *workerNode) connectLoop() error {
	// Release signal notification resources on exit.
	defer wn.cancel()
	// Use a closure so the defer always stops the current sync service,
	// even after reconnect replaces it with a fresh instance.
	defer func() { wn.syncSvc.Stop() }()

	for {
		wn.connStatus.Store("connecting")
		err := wn.ws.Connect(wn.ctx)

		wn.updateConnStatus(err)
		// Clean up all local workers from the previous connection
		// before retrying, to prevent goroutine/resource leaks.
		wn.bridge.ShutdownAll(context.Background())
		wn.resetSyncState()

		if wn.ctx.Err() != nil {
			break
		}
		if errors.Is(err, cluster.ErrApprovalRevoked) {
			wn.logger.Warn("approval revoked by leader, stopping reconnection")
			// Block until context is cancelled (user must disconnect via UI).
			<-wn.ctx.Done()
			break
		}

		// Create a fresh sync service for the next connection attempt (Stop is one-shot).
		wn.syncSvc = wn.newFileSyncService()

		if errors.Is(err, cluster.ErrPendingApproval) {
			wn.logger.Info("awaiting approval from leader, will retry...")
		} else {
			wn.logger.Warn("disconnected from leader, reconnecting...", "error", err)
		}
		select {
		case <-time.After(workerReconnectDelay):
		case <-wn.ctx.Done():
		}
		if wn.ctx.Err() != nil {
			break
		}

		wn.reResolveLeader()
	}

	wn.logger.Info("worker node shutting down")
	if wn.restartRequested.Load() {
		return errRestartRequested
	}
	return nil
}

// updateConnStatus sets the connection status based on the disconnect error.
func (wn *workerNode) updateConnStatus(err error) {
	switch {
	case errors.Is(err, cluster.ErrApprovalRevoked):
		wn.connStatus.Store("revoked")
	case errors.Is(err, cluster.ErrPendingApproval):
		wn.connStatus.Store("pending")
	default:
		wn.connStatus.Store("disconnected")
	}
}

// resetSyncState stops the file watcher and resets pipe state for a clean reconnect.
func (wn *workerNode) resetSyncState() {
	wn.syncSvc.Stop()
	wn.syncMu.Lock()
	if wn.syncPipeW != nil {
		wn.syncPipeW.CloseWithError(fmt.Errorf("disconnected"))
		wn.syncPipeW = nil
		wn.syncDoneCh = nil
	}
	wn.syncMu.Unlock()
}

// reResolveLeader checks tracker discovery for an updated leader address.
func (wn *workerNode) reResolveLeader() {
	if wn.discovery == nil {
		return
	}
	wn.discovery.Announce(wn.ctx)
	if newAddr := wn.discovery.LeaderAddr(); newAddr != "" && newAddr != wn.ws.LeaderAddr() {
		wn.logger.Info("leader address changed via tracker", "old", wn.ws.LeaderAddr(), "new", newAddr)
		wn.ws.SetLeaderAddr(newAddr)
	}
}

// newFileSyncService creates a file sync service for receiving updates from leader.
func (wn *workerNode) newFileSyncService() *cluster.FileSyncService {
	return cluster.NewFileSyncService(cluster.FileSyncConfig{
		RootDir:  wn.rootDir,
		SyncDirs: []string{"agents", "skills", "workspace"},
		NodeID:   wn.nodeName,
		SendFn: func(update *pb.FileUpdate) error {
			return wn.ws.Send(&pb.NodeMessage{
				Msg: &pb.NodeMessage_FileUpdate{FileUpdate: update},
			})
		},
		Logger: wn.logger,
	})
}

// wireFileSyncHandlers sets up the file sync and incremental update handlers
// on the worker stream. Uses io.Pipe so tar extraction happens on the fly,
// avoiding unbounded memory usage for large workspaces.
func (wn *workerNode) wireFileSyncHandlers() {
	wn.ws.SetFileSyncHandler(func(_ context.Context, msg *pb.FileSyncData) {
		wn.syncMu.Lock()
		defer wn.syncMu.Unlock()

		// Lazily create the pipe on the first chunk.
		if wn.syncPipeW == nil {
			pr, pw := io.Pipe()
			wn.syncPipeW = pw
			wn.syncDoneCh = make(chan error, 1)
			go func() {
				wn.syncDoneCh <- wn.syncSvc.ApplyInitialSyncStream(pr)
			}()
		}

		if len(msg.Data) > 0 && wn.syncPipeW != nil {
			if _, err := wn.syncPipeW.Write(msg.Data); err != nil {
				// Extraction failed (e.g. corrupt archive). Close the pipe
				// and abandon this sync — the next reconnect will retry.
				wn.logger.Error("sync pipe broken, aborting initial sync", "error", err)
				wn.syncPipeW.CloseWithError(err)
				wn.syncPipeW = nil
				wn.syncDoneCh = nil
				return
			}
		}

		if msg.Final && wn.syncPipeW != nil {
			wn.syncPipeW.Close()
			if err := <-wn.syncDoneCh; err != nil {
				wn.logger.Error("failed to apply initial sync", "error", err)
			}
			wn.syncPipeW = nil
			wn.syncDoneCh = nil
			wn.logger.Info("initial file sync complete")

			// Start the filesystem watcher AFTER initial sync is fully
			// applied. Starting earlier would cause every extracted file
			// to echo back as a "new change" to the leader.
			go func() {
				if err := wn.syncSvc.WatchAndSync(); err != nil {
					wn.logger.Warn("file sync watcher stopped", "error", err)
				}
			}()
		}
	})

	wn.ws.SetFileUpdateHandler(func(_ context.Context, msg *pb.FileUpdate) {
		if err := wn.syncSvc.ApplyFileUpdate(msg); err != nil {
			wn.logger.Warn("failed to apply file update", "path", msg.Path, "error", err)
		}
	})
}

// discoveryRelayURL returns the relay URL from discovery, or empty string.
func (wn *workerNode) discoveryRelayURL() string {
	if wn.discovery != nil {
		return wn.discovery.RelayURL()
	}
	return ""
}

// happyEyeballs tries a direct TCP connection first, then falls back to
// the relay after a short delay. First successful connection wins.
func happyEyeballs(ctx context.Context, directAddr, relayAddr, swarmCode string, identity *cluster.NodeIdentity, logger *slog.Logger) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
		via  string
	}
	ch := make(chan result, happyEyeballsAttempts)

	dialCtx, dialCancel := context.WithTimeout(ctx, happyEyeballsTimeout)
	defer dialCancel()

	// Direct attempt — starts immediately.
	go func() {
		dialer := &net.Dialer{Timeout: happyEyeballsDialTimeout}
		conn, err := dialer.DialContext(dialCtx, "tcp", directAddr)
		ch <- result{conn, err, "direct"}
	}()

	// Relay attempt — starts after 500ms delay to prefer direct.
	go func() {
		select {
		case <-time.After(happyEyeballsRelayDelay):
		case <-dialCtx.Done():
			ch <- result{nil, dialCtx.Err(), "relay"}
			return
		}
		conn, err := cluster.DialRelay(dialCtx, relayAddr, swarmCode, identity)
		ch <- result{conn, err, "relay"}
	}()

	// Take first success.
	var firstErr error
	for range happyEyeballsAttempts {
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
