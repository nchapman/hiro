package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/api"
	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
	"github.com/nchapman/hiro/internal/platform"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/platform/loghandler"
	"github.com/nchapman/hiro/internal/uidpool"
	"github.com/nchapman/hiro/internal/watcher"
	"github.com/nchapman/hiro/web"
)

const (
	// maxRestarts caps the number of consecutive rapid restarts before exiting.
	maxRestarts = 3

	// readHeaderTimeout limits how long the HTTP server waits for request headers.
	readHeaderTimeout = 5 * time.Second

	// httpIdleTimeout is how long the HTTP server keeps idle connections open.
	httpIdleTimeout = 120 * time.Second

	// shutdownTimeout is the grace period for HTTP server shutdown.
	shutdownTimeout = 10 * time.Second

	// logPruneInterval is how often old logs are pruned from the database.
	logPruneInterval = 1 * time.Hour

	// logRetention is how long logs are kept before pruning.
	logRetention = 7 * 24 * time.Hour
)

// errRestartRequested is returned by run() when the setup API requests a
// process restart (e.g. after switching to worker mode during onboarding).
var errRestartRequested = fmt.Errorf("restart requested")

func main() {
	// Dispatch subcommand: "hiro agent" runs an agent worker process.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := runAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Restart loop: the setup API can request a restart when the user picks
	// worker mode during onboarding, or when a worker disconnects from
	// the cluster. The counter resets after a run lasts long enough to
	// distinguish a real session from a crash loop.
	restarts := 0
	for {
		start := time.Now()
		err := run()
		if errors.Is(err, errRestartRequested) {
			if time.Since(start) > 30*time.Second {
				restarts = 0 // ran long enough — not a crash loop
			}
			restarts++
			if restarts > maxRestarts {
				fmt.Fprintf(os.Stderr, "error: too many restarts\n")
				os.Exit(1)
			}
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}
}

// app holds shared state for the leader process lifecycle.
type app struct {
	rootDir    string
	absRootDir string
	listenAddr string

	logger *slog.Logger
	pdb    *platformdb.DB
	lh     *loghandler.Handler
	cp     *controlplane.ControlPlane

	fsWatcher *watcher.Watcher
	srv       *api.Server
	mgr       *agent.Manager

	// Cluster state — only populated for leader mode.
	cs             clusterState
	clusterSvc     *cluster.LeaderService
	clusterStarted bool
	relayLis       *cluster.ChannelListener

	// Contexts for lifecycle management.
	ctx             context.Context
	cancel          context.CancelFunc
	discoveryCtx    context.Context
	discoveryCancel context.CancelFunc
	pruneCtx        context.Context
	pruneCancel     context.CancelFunc

	restartCh chan struct{}
}

func run() error {
	a, err := initPlatform()
	if err != nil {
		return err
	}
	defer a.close()

	// Check cluster mode: worker nodes take a completely different path.
	if a.cp.ClusterMode() == "worker" {
		return runWorkerNode(a.absRootDir, a.cp, a.logger)
	}

	if err := a.initServer(); err != nil {
		return err
	}

	// Boot cluster + manager if already configured.
	mode := a.cp.ClusterMode()
	if mode == "leader" {
		if err := a.startCluster(); err != nil {
			return fmt.Errorf("starting cluster: %w", err)
		}
	}
	if a.cp.IsConfigured() {
		if err := a.startManager(); err != nil {
			return fmt.Errorf("starting agent manager: %w", err)
		}
	} else {
		a.logger.Info("no LLM provider configured — waiting for setup via web UI")
	}

	return a.serve()
}

// initPlatform sets up the foundational infrastructure: env, logging, dirs, DB,
// and control plane config. The returned app must be closed via app.close().
func initPlatform() (*app, error) {
	// Load .env file if present (does not override existing env vars).
	_ = godotenv.Load()

	// Parse log level from environment (default INFO).
	logLevel := slog.LevelInfo
	if lvl := os.Getenv("HIRO_LOG_LEVEL"); lvl != "" {
		if err := logLevel.UnmarshalText([]byte(lvl)); err != nil {
			return nil, fmt.Errorf("invalid HIRO_LOG_LEVEL %q: %w", lvl, err)
		}
	}

	// Temporary stdout-only logger for pre-DB initialization.
	bootLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	rootDir := envOr("HIRO_ROOT", ".")
	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolving root dir: %w", err)
	}

	if err := platform.Init(rootDir, bootLogger); err != nil {
		return nil, fmt.Errorf("initializing platform: %w", err)
	}

	pdb, err := platformdb.Open(filepath.Join(absRootDir, "db", "hiro.db"))
	if err != nil {
		return nil, fmt.Errorf("opening platform database: %w", err)
	}

	lh := loghandler.New(pdb, os.Stdout, logLevel)
	logger := slog.New(lh)

	cpPath := filepath.Join(absRootDir, "config", "config.yaml")
	cp, err := controlplane.Load(cpPath, logger)
	if err != nil {
		pdb.Close()
		lh.Close()
		return nil, fmt.Errorf("loading control plane: %w", err)
	}

	a := &app{
		rootDir:    rootDir,
		absRootDir: absRootDir,
		listenAddr: envOr("HIRO_ADDR", ":8080"),
		logger:     logger,
		pdb:        pdb,
		lh:         lh,
		cp:         cp,
		restartCh:  make(chan struct{}, 1),
	}

	return a, nil
}

// initServer sets up the HTTP API server, signal handling, log pruning, config
// watcher, and exposes callbacks for the setup API.
func (a *app) initServer() error {
	// Start filesystem watcher for HIRO_ROOT (leader-only — workers don't need it).
	fsWatcher, err := watcher.New(a.absRootDir, a.logger)
	if err != nil {
		return fmt.Errorf("starting filesystem watcher: %w", err)
	}
	a.fsWatcher = fsWatcher

	// Set up signal handling so the manager gets a cancellable context.
	a.ctx, a.cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// Prune old logs periodically.
	a.pruneCtx, a.pruneCancel = context.WithCancel(context.Background())
	go a.pruneLogsLoop()

	// Discovery context for cluster tracker.
	a.discoveryCtx, a.discoveryCancel = context.WithCancel(a.ctx)

	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("loading web UI: %w", err)
	}

	a.srv = api.NewServer(a.logger, webFS, a.cp, a.pdb, a.absRootDir)
	a.srv.InitTerminalSessions()
	a.srv.SetWatcher(a.fsWatcher)
	a.srv.SetLogHandler(a.lh)

	// Reload config.yaml when it changes on disk (external edits, coordinator writes).
	a.fsWatcher.Subscribe("config/config.yaml", func(events []watcher.Event) {
		if err := a.cp.Reload(); err != nil {
			a.logger.Warn("failed to reload config.yaml", "error", err)
			return
		}
		if a.mgr != nil {
			a.mgr.PushConfigUpdateAll()
		}
	})

	// Expose callbacks so the setup API can trigger cluster/manager start
	// during onboarding (before providers are configured at boot time).
	a.srv.SetStartManager(a.startManager)
	a.srv.SetStartCluster(a.startCluster)
	a.srv.SetRestartFunc(func() {
		select {
		case a.restartCh <- struct{}{}:
		default:
		}
	})

	return nil
}

// startCluster boots the cluster gRPC server, identity, and tracker discovery.
// Idempotent — calling when already started is a no-op.
func (a *app) startCluster() error {
	if a.clusterStarted {
		return nil
	}

	identity, tlsCert, err := setupNodeIdentity(a.absRootDir, a.logger)
	if err != nil {
		return err
	}

	a.cs, err = setupClusterServer(a.absRootDir, tlsCert, a.cp, a.logger)
	if err != nil {
		return err
	}
	a.clusterSvc = a.cs.service

	if err := a.startTrackerDiscovery(identity, tlsCert); err != nil {
		return err
	}

	a.clusterStarted = true
	a.srv.SetNodeRegistry(a.cs.registry)
	a.srv.SetPendingRegistry(a.cs.pending)
	if ts := a.srv.TerminalSessions(); ts != nil {
		api.WireClusterTerminal(ts, a.clusterSvc)
	}
	a.srv.SetDisconnectNode(func(nodeID string) {
		a.clusterSvc.KillWorkersOnNode(nodeID)
		a.cs.leaderStream.DisconnectNode(nodeID)
	})
	return nil
}

// startTrackerDiscovery starts tracker-based discovery and relay connectivity
// if a tracker URL is configured. Called as part of startCluster. No-op if
// no tracker URL is set.
func (a *app) startTrackerDiscovery(identity *cluster.NodeIdentity, tlsCert tls.Certificate) error {
	trackerURL := a.cp.ClusterTrackerURL()
	if trackerURL == "" {
		return nil
	}

	swarmCode := a.cp.ClusterSwarmCode()
	if swarmCode == "" {
		return fmt.Errorf("cluster.swarm_code (or HIRO_SWARM_CODE) is required when tracker_url is set")
	}

	_, portStr, _ := net.SplitHostPort(a.cs.listener.Addr().String())
	grpcPort, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("parsing gRPC port %q: %w", portStr, err)
	}

	nodeName := a.cp.ClusterNodeName()
	if nodeName == "" {
		nodeName = envOr("HOSTNAME", "leader")
	}

	dc := cluster.NewDiscoveryClient(cluster.DiscoveryConfig{
		TrackerURL:     trackerURL,
		SwarmCode:      swarmCode,
		Role:           "leader",
		GRPCPort:       grpcPort,
		Identity:       identity,
		TLSFingerprint: cluster.TLSFingerprint(tlsCert),
		NodeName:       nodeName,
		Logger:         a.logger,
	})

	go dc.Run(a.discoveryCtx)
	a.logger.Info("tracker discovery started", "tracker", trackerURL, "role", "leader")

	a.relayLis = cluster.NewChannelListener(a.cs.listener.Addr())
	go func() {
		if err := a.cs.grpcServer.Serve(a.relayLis); err != nil {
			a.logger.Error("relay gRPC server error", "error", err)
		}
	}()

	go a.connectRelay(dc, grpcPort, swarmCode, identity, tlsCert)

	return nil
}

// connectRelay waits for the leader's public IP from the tracker, tests
// reachability, and falls back to relay connectivity if needed.
func (a *app) connectRelay(dc *cluster.DiscoveryClient, grpcPort int, swarmCode string, identity *cluster.NodeIdentity, tlsCert tls.Certificate) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for dc.YourIP() == "" {
		select {
		case <-ticker.C:
		case <-a.discoveryCtx.Done():
			return
		}
	}

	publicAddr := fmt.Sprintf("%s:%d", dc.YourIP(), grpcPort)
	if cluster.SelfTestReachability(publicAddr, tlsCert) {
		a.logger.Info("leader is publicly reachable", "addr", publicAddr)
		return
	}

	relayURL := dc.RelayURL()
	if relayURL == "" {
		a.logger.Warn("leader is NOT publicly reachable and no relay is configured",
			"addr", publicAddr)
		return
	}

	a.logger.Info("leader is NOT publicly reachable, connecting to relay",
		"addr", publicAddr, "relay", relayURL)

	a.cs.leaderStream.SetRelayAddr(relayURL)

	rc := cluster.NewRelayClient(cluster.RelayConfig{
		RelayAddr: relayURL,
		SwarmCode: swarmCode,
		Identity:  identity,
		Logger:    a.logger,
	})

	rc.Run(a.discoveryCtx, func(conn net.Conn) {
		a.relayLis.Enqueue(conn)
	})
}

// startManager boots the agent manager and coordinator. Idempotent.
func (a *app) startManager() error {
	if a.mgr != nil {
		return nil
	}
	if !a.cp.IsConfigured() {
		return fmt.Errorf("no LLM provider configured")
	}

	pool, err := detectUIDPool(a.logger)
	if err != nil {
		return err
	}

	a.mgr = agent.NewManager(a.ctx, a.rootDir, agent.Options{
		WorkingDir: a.absRootDir,
	}, a.cp, a.logger, nil, pool, a.pdb)
	if a.clusterSvc != nil {
		a.mgr.SetClusterService(a.clusterSvc)
	}

	a.mgr.WatchAgentDefinitions(a.fsWatcher)

	if err := a.mgr.RestoreInstances(a.ctx); err != nil {
		a.logger.Warn("failed to restore some agent instances", "error", err)
	}

	leaderID, err := bootstrapCoordinator(a.ctx, a.mgr, a.logger)
	if err != nil {
		return err
	}
	if leaderID != "" {
		providerType, _, _, _ := a.cp.ProviderInfo()
		a.logger.Info("leader agent ready",
			"id", leaderID,
			"provider", providerType,
		)
		a.srv.SetManager(a.mgr, leaderID)
	}

	return nil
}

// serve starts the HTTP server and blocks until shutdown signal or restart.
func (a *app) serve() error {
	httpServer := &http.Server{
		Addr:              a.listenAddr,
		Handler:           a.srv,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	go func() {
		clusterInfo := ""
		if a.clusterStarted {
			clusterInfo = a.cs.listener.Addr().String()
		}
		a.logger.Info("hiro starting", "addr", a.listenAddr, "mode", a.cp.ClusterMode(), "cluster", clusterInfo)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("server error", "error", err)
			a.cancel()
		}
	}()

	// Wait for shutdown signal or restart request.
	var runErr error
	select {
	case <-a.ctx.Done():
		a.logger.Info("shutting down...")
	case <-a.restartCh:
		a.logger.Info("restarting for mode change...")
		runErr = errRestartRequested
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	_ = httpServer.Shutdown(shutdownCtx)
	return runErr
}

// shutdown tears down all subsystems in order. Idempotent and safe to call
// even if initServer was never reached (all fields are nil-guarded).
func (a *app) shutdown() {
	if a.discoveryCancel != nil {
		a.discoveryCancel()
	}
	if a.relayLis != nil {
		a.relayLis.Close()
	}
	if a.cs.grpcServer != nil {
		a.cs.grpcServer.GracefulStop()
	}
	if a.cs.fileSync != nil {
		a.cs.fileSync.Stop()
	}
	if a.srv != nil {
		a.srv.ShutdownTerminalSessions()
	}
	if a.mgr != nil {
		a.mgr.Shutdown()
	}
	if saveErr := a.cp.Save(); saveErr != nil {
		a.logger.Error("failed to save control plane config", "error", saveErr)
	}
}

// close tears down all subsystems and releases foundational resources.
// Safe to call even if initServer/startCluster were never reached —
// shutdown() nil-guards all optional fields.
func (a *app) close() {
	a.shutdown()
	if a.cancel != nil {
		a.cancel()
	}
	if a.pruneCancel != nil {
		a.pruneCancel()
	}
	// discoveryCtx is a child of ctx — cancelled automatically above.
	if a.fsWatcher != nil {
		a.fsWatcher.Close()
	}
	a.lh.Close()
	a.pdb.Close()
}

// pruneLogsLoop periodically removes old logs from the database.
func (a *app) pruneLogsLoop() {
	ticker := time.NewTicker(logPruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.pruneCtx.Done():
			return
		case <-ticker.C:
			if n, err := a.pdb.PruneLogs(a.pruneCtx, logRetention); err != nil {
				a.logger.Warn("failed to prune logs", "error", err)
			} else if n > 0 {
				a.logger.Info("pruned old logs", "count", n)
			}
		}
	}
}

// detectUIDPool checks for the hiro-agents Unix group and sets up the UID pool
// for process isolation. Returns nil if isolation is not available.
func detectUIDPool(logger *slog.Logger) (*uidpool.Pool, error) {
	grp, err := user.LookupGroup("hiro-agents")
	if err != nil {
		return nil, nil //nolint:nilerr // group not found means isolation is disabled
	}

	gid, err := strconv.ParseUint(grp.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parsing hiro-agents GID %q: %w", grp.Gid, err)
	}
	pool := uidpool.New(uidpool.DefaultBaseUID, uint32(gid), uidpool.DefaultSize)
	logger.Info("unix user isolation enabled", "pool_size", uidpool.DefaultSize)

	if coordGrp, err := user.LookupGroup("hiro-coordinators"); err == nil {
		coordGID, err := strconv.ParseUint(coordGrp.Gid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parsing hiro-coordinators GID %q: %w", coordGrp.Gid, err)
		}
		pool.SetGroupGID("hiro-coordinators", uint32(coordGID))
		logger.Info("coordinator group detected", "gid", coordGID)
	}

	return pool, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
