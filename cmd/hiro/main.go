package main

import (
	"context"
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
		if err == errRestartRequested {
			if time.Since(start) > 30*time.Second {
				restarts = 0 // ran long enough — not a crash loop
			}
			restarts++
			if restarts > 3 {
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

func run() error {
	// Load .env file if present (does not override existing env vars)
	godotenv.Load()

	// Parse log level from environment (default INFO).
	logLevel := slog.LevelInfo
	if lvl := os.Getenv("HIRO_LOG_LEVEL"); lvl != "" {
		if err := logLevel.UnmarshalText([]byte(lvl)); err != nil {
			return fmt.Errorf("invalid HIRO_LOG_LEVEL %q: %w", lvl, err)
		}
	}

	// Temporary stdout-only logger for pre-DB initialization.
	bootLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	listenAddr := envOr("HIRO_ADDR", ":8080")
	rootDir := envOr("HIRO_ROOT", ".")

	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolving root dir: %w", err)
	}
	cpPath := filepath.Join(absRootDir, "config", "config.yaml")

	// Initialize platform directory structure and seed defaults.
	if err := platform.Init(rootDir, bootLogger); err != nil {
		return fmt.Errorf("initializing platform: %w", err)
	}

	// Open the unified platform database.
	pdb, err := platformdb.Open(filepath.Join(absRootDir, "db", "hiro.db"))
	if err != nil {
		return fmt.Errorf("opening platform database: %w", err)
	}
	defer pdb.Close()

	// Create the log handler that tees to stdout + SQLite, then build the logger.
	lh := loghandler.New(pdb, os.Stdout, logLevel)
	defer lh.Close()
	logger := slog.New(lh)

	// Load control plane config (secrets, tool policies, providers).
	cp, err := controlplane.Load(cpPath, logger)
	if err != nil {
		return fmt.Errorf("loading control plane: %w", err)
	}

	// Check cluster mode: worker nodes take a completely different path.
	if cp.ClusterMode() == "worker" {
		return runWorkerNode(absRootDir, cp, logger)
	}

	// Prune old logs periodically (every hour, 7-day retention).
	// Only runs on leader nodes — workers don't serve the log UI.
	pruneCtx, pruneCancel := context.WithCancel(context.Background())
	defer pruneCancel()
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-pruneCtx.Done():
				return
			case <-ticker.C:
				if n, err := pdb.PruneLogs(7 * 24 * time.Hour); err != nil {
					logger.Warn("failed to prune logs", "error", err)
				} else if n > 0 {
					logger.Info("pruned old logs", "count", n)
				}
			}
		}
	}()

	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("loading web UI: %w", err)
	}

	// Start filesystem watcher for HIRO_ROOT.
	fsWatcher, err := watcher.New(absRootDir, logger)
	if err != nil {
		return fmt.Errorf("starting filesystem watcher: %w", err)
	}
	defer fsWatcher.Close()

	// Set up signal handling early so the manager gets a cancellable context
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := api.NewServer(logger, webFS, cp, pdb, absRootDir)
	srv.InitTerminalSessions()
	srv.SetWatcher(fsWatcher)
	srv.SetLogHandler(lh)

	// Cluster state — only populated for leader mode.
	var cs clusterState
	var clusterSvc *cluster.LeaderService
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)
	defer discoveryCancel()
	var relayLis *cluster.ChannelListener

	// startCluster boots the cluster gRPC server, identity, and tracker discovery.
	// Called at startup for existing leader configs, or by the setup API when the
	// user picks leader mode during onboarding. Idempotent.
	clusterStarted := false
	startCluster := func() error {
		if clusterStarted {
			return nil
		}

		identity, tlsCert, err := setupNodeIdentity(absRootDir, logger)
		if err != nil {
			return err
		}

		cs, err = setupClusterServer(absRootDir, tlsCert, cp, logger)
		if err != nil {
			return err
		}
		clusterSvc = cs.service

		// Start tracker discovery if configured.
		if trackerURL := cp.ClusterTrackerURL(); trackerURL != "" {
			swarmCode := cp.ClusterSwarmCode()
			if swarmCode == "" {
				return fmt.Errorf("cluster.swarm_code (or HIRO_SWARM_CODE) is required when tracker_url is set")
			}

			_, portStr, _ := net.SplitHostPort(cs.listener.Addr().String())
			grpcPort, err := strconv.Atoi(portStr)
			if err != nil {
				return fmt.Errorf("parsing gRPC port %q: %w", portStr, err)
			}

			nodeName := cp.ClusterNodeName()
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
				Logger:         logger,
			})

			go dc.Run(discoveryCtx)
			logger.Info("tracker discovery started", "tracker", trackerURL, "role", "leader")

			relayLis = cluster.NewChannelListener(cs.listener.Addr())
			go cs.grpcServer.Serve(relayLis)

			go func() {
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()
				for dc.YourIP() == "" {
					select {
					case <-ticker.C:
					case <-discoveryCtx.Done():
						return
					}
				}

				publicAddr := fmt.Sprintf("%s:%d", dc.YourIP(), grpcPort)
				if cluster.SelfTestReachability(publicAddr, tlsCert) {
					logger.Info("leader is publicly reachable", "addr", publicAddr)
					return
				}

				relayURL := dc.RelayURL()
				if relayURL == "" {
					logger.Warn("leader is NOT publicly reachable and no relay is configured",
						"addr", publicAddr)
					return
				}

				logger.Info("leader is NOT publicly reachable, connecting to relay",
					"addr", publicAddr, "relay", relayURL)

				cs.leaderStream.SetRelayAddr(relayURL)

				rc := cluster.NewRelayClient(cluster.RelayConfig{
					RelayAddr: relayURL,
					SwarmCode: swarmCode,
					Identity:  identity,
					Logger:    logger,
				})

				rc.Run(discoveryCtx, func(conn net.Conn) {
					relayLis.Enqueue(conn)
				})
			}()
		}

		clusterStarted = true
		srv.SetNodeRegistry(cs.registry)
		srv.SetPendingRegistry(cs.pending)
		if ts := srv.TerminalSessions(); ts != nil {
			api.WireClusterTerminal(ts, clusterSvc)
		}
		srv.SetDisconnectNode(func(nodeID string) {
			nid := cluster.NodeID(nodeID)
			clusterSvc.KillWorkersOnNode(nid)
			cs.leaderStream.DisconnectNode(nid)
		})
		return nil
	}

	// Shared state for the manager lifecycle — the manager can be started
	// at boot (if providers are configured) or later via the setup API.
	var mgr *agent.Manager

	// Reload config.yaml when it changes on disk (external edits, coordinator writes).
	fsWatcher.Subscribe("config/config.yaml", func(events []watcher.Event) {
		if err := cp.Reload(); err != nil {
			logger.Warn("failed to reload config.yaml", "error", err)
			return
		}
		if mgr != nil {
			mgr.PushConfigUpdateAll()
		}
	})

	// startManager boots the agent manager and coordinator.
	// Idempotent — calling it when a manager already exists is a no-op.
	startManager := func() error {
		if mgr != nil {
			return nil // already started
		}
		if !cp.IsConfigured() {
			return fmt.Errorf("no LLM provider configured")
		}

		// Detect Unix user isolation.
		var pool *uidpool.Pool
		if grp, err := user.LookupGroup("hiro-agents"); err == nil {
			gid, err := strconv.ParseUint(grp.Gid, 10, 32)
			if err != nil {
				return fmt.Errorf("parsing hiro-agents GID %q: %w", grp.Gid, err)
			}
			pool = uidpool.New(uidpool.DefaultBaseUID, uint32(gid), uidpool.DefaultSize)
			logger.Info("unix user isolation enabled", "pool_size", uidpool.DefaultSize)

			if coordGrp, err := user.LookupGroup("hiro-coordinators"); err == nil {
				coordGID, err := strconv.ParseUint(coordGrp.Gid, 10, 32)
				if err != nil {
					return fmt.Errorf("parsing hiro-coordinators GID %q: %w", coordGrp.Gid, err)
				}
				pool.SetGroupGID("hiro-coordinators", uint32(coordGID))
				logger.Info("coordinator group detected", "gid", coordGID)
			}
		}

		mgr = agent.NewManager(ctx, rootDir, agent.Options{
			WorkingDir: absRootDir,
		}, cp, logger, nil, pool, pdb)
		if clusterSvc != nil {
			mgr.SetClusterService(clusterSvc)
		}

		mgr.WatchAgentDefinitions(fsWatcher)

		if err := mgr.RestoreInstances(ctx); err != nil {
			logger.Warn("failed to restore some agent instances", "error", err)
		}

		leaderID, err := bootstrapCoordinator(ctx, mgr, logger)
		if err != nil {
			return err
		}
		if leaderID != "" {
			providerType, _, _, _ := cp.ProviderInfo()
			logger.Info("leader agent ready",
				"id", leaderID,
				"provider", providerType,
			)
			srv.SetManager(mgr, leaderID)
		}

		return nil
	}

	// Restart channel: the setup API signals this when the user picks worker
	// mode during onboarding, triggering a clean shutdown + re-run.
	restartCh := make(chan struct{}, 1)

	// Expose callbacks so the setup API can trigger them.
	srv.SetStartManager(startManager)
	srv.SetStartCluster(startCluster)
	srv.SetRestartFunc(func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	})

	// Boot cluster + manager if already configured.
	mode := cp.ClusterMode()
	if mode == "leader" {
		if err := startCluster(); err != nil {
			return fmt.Errorf("starting cluster: %w", err)
		}
	}
	if cp.IsConfigured() {
		if err := startManager(); err != nil {
			return fmt.Errorf("starting agent manager: %w", err)
		}
	} else {
		logger.Info("no LLM provider configured — waiting for setup via web UI")
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		clusterInfo := ""
		if clusterStarted {
			clusterInfo = cs.listener.Addr().String()
		}
		logger.Info("hiro starting", "addr", listenAddr, "mode", mode, "cluster", clusterInfo)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal or restart request.
	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutting down...")
	case <-restartCh:
		logger.Info("restarting for mode change...")
		runErr = errRestartRequested
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	_ = httpServer.Shutdown(shutdownCtx)
	discoveryCancel()
	if relayLis != nil {
		relayLis.Close()
	}
	if cs.grpcServer != nil {
		cs.grpcServer.GracefulStop()
	}
	if cs.fileSync != nil {
		cs.fileSync.Stop()
	}
	srv.ShutdownTerminalSessions()
	if mgr != nil {
		mgr.Shutdown()
	}
	if saveErr := cp.Save(); saveErr != nil {
		logger.Error("failed to save control plane config", "error", saveErr)
	}
	return runErr
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}


