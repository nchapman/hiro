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

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/api"
	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/controlplane"
	"github.com/nchapman/hivebot/internal/platform"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
	"github.com/nchapman/hivebot/internal/platform/loghandler"
	"github.com/nchapman/hivebot/internal/uidpool"
	"github.com/nchapman/hivebot/internal/watcher"
	"github.com/nchapman/hivebot/web"
)

func main() {
	// Dispatch subcommand: "hive agent" runs an agent worker process.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := runAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load .env file if present (does not override existing env vars)
	godotenv.Load()

	// Parse log level from environment (default INFO).
	logLevel := slog.LevelInfo
	if lvl := os.Getenv("HIVE_LOG_LEVEL"); lvl != "" {
		if err := logLevel.UnmarshalText([]byte(lvl)); err != nil {
			return fmt.Errorf("invalid HIVE_LOG_LEVEL %q: %w", lvl, err)
		}
	}

	// Temporary stdout-only logger for pre-DB initialization.
	bootLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	listenAddr := envOr("HIVE_ADDR", ":8080")
	rootDir := envOr("HIVE_ROOT", ".")

	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolving root dir: %w", err)
	}
	cpPath := filepath.Join(absRootDir, "config.yaml")

	// Initialize platform directory structure and seed defaults.
	if err := platform.Init(rootDir, bootLogger); err != nil {
		return fmt.Errorf("initializing platform: %w", err)
	}

	// Open the unified platform database.
	pdb, err := platformdb.Open(filepath.Join(absRootDir, "db", "hive.db"))
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

	// Start filesystem watcher for HIVE_ROOT.
	fsWatcher, err := watcher.New(absRootDir, logger)
	if err != nil {
		return fmt.Errorf("starting filesystem watcher: %w", err)
	}
	defer fsWatcher.Close()

	// Set up signal handling early so the manager gets a cancellable context
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := api.NewServer(logger, webFS, cp, pdb, absRootDir)
	srv.SetWatcher(fsWatcher)
	srv.SetLogHandler(lh)

	// Set up node identity (Ed25519 keypair + mTLS certificate).
	identity, tlsCert, err := setupNodeIdentity(absRootDir, logger)
	if err != nil {
		return err
	}

	// Start cluster gRPC server for worker node connections.
	cs, err := setupClusterServer(absRootDir, tlsCert, cp, logger)
	if err != nil {
		return err
	}
	clusterSvc := cs.service

	// Start tracker discovery announcements if configured.
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)
	defer discoveryCancel()
	var relayLis *cluster.ChannelListener // closed on shutdown if set
	if trackerURL := cp.ClusterTrackerURL(); trackerURL != "" {
		swarmCode := cp.ClusterSwarmCode()
		if swarmCode == "" {
			return fmt.Errorf("cluster.swarm_code (or HIVE_SWARM_CODE) is required when tracker_url is set")
		}

		// Parse gRPC port from cluster address.
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

		// Create a shared listener for relayed connections. gRPC's Serve()
		// is called once and accepts connections as they arrive via Enqueue().
		relayLis = cluster.NewChannelListener(cs.listener.Addr())
		go cs.grpcServer.Serve(relayLis)

		// After first announce, check if we're reachable. If not, register
		// with the relay so workers can reach us through it.
		go func() {
			// Poll until we have our public IP from the tracker.
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

			rc := cluster.NewRelayClient(cluster.RelayConfig{
				RelayAddr: relayURL,
				SwarmCode: swarmCode,
				Identity:  identity,
				Logger:    logger,
			})

			// Relayed connections feed into the shared gRPC listener.
			// The gRPC server handles mTLS on each connection.
			rc.Run(discoveryCtx, func(conn net.Conn) {
				relayLis.Enqueue(conn)
			})
		}()
	}

	// Shared state for the manager lifecycle — the manager can be started
	// at boot (if providers are configured) or later via the setup API.
	var mgr *agent.Manager

	// Reload config.yaml when it changes on disk (external edits, coordinator writes).
	// After reloading, push resolved config to all running agents since provider,
	// model defaults, or tool policies may have changed.
	fsWatcher.Subscribe("config.yaml", func(events []watcher.Event) {
		if err := cp.Reload(); err != nil {
			logger.Warn("failed to reload config.yaml", "error", err)
			return
		}
		if mgr != nil {
			mgr.PushConfigUpdateAll()
		}
	})

	// startManager boots the agent manager and coordinator.
	// It is idempotent — calling it when a manager already exists is a no-op.
	startManager := func() error {
		if mgr != nil {
			return nil // already started
		}
		if !cp.IsConfigured() {
			return fmt.Errorf("no LLM provider configured")
		}

		// Detect Unix user isolation: enabled iff the hive-agents group exists.
		var pool *uidpool.Pool
		if grp, err := user.LookupGroup("hive-agents"); err == nil {
			gid, err := strconv.ParseUint(grp.Gid, 10, 32)
			if err != nil {
				return fmt.Errorf("parsing hive-agents GID %q: %w", grp.Gid, err)
			}
			pool = uidpool.New(uidpool.DefaultBaseUID, uint32(gid), uidpool.DefaultSize)
			logger.Info("unix user isolation enabled", "pool_size", uidpool.DefaultSize)

			// Detect hive-coordinators group for agents/ and skills/ write access.
			if coordGrp, err := user.LookupGroup("hive-coordinators"); err == nil {
				coordGID, err := strconv.ParseUint(coordGrp.Gid, 10, 32)
				if err != nil {
					return fmt.Errorf("parsing hive-coordinators GID %q: %w", coordGrp.Gid, err)
				}
				pool.SetCoordinatorGID(uint32(coordGID))
				logger.Info("coordinator group detected", "gid", coordGID)
			}
		}

		mgr = agent.NewManager(ctx, rootDir, agent.Options{
			WorkingDir: absRootDir,
		}, cp, logger, nil, pool, pdb)
		mgr.SetClusterService(clusterSvc)

		// Watch agent definitions for config changes and push to running agents.
		mgr.WatchAgentDefinitions(fsWatcher)

		// Restore any persistent instances from previous run
		if err := mgr.RestoreInstances(ctx); err != nil {
			logger.Warn("failed to restore some agent instances", "error", err)
		}

		// Ensure the coordinator agent is running.
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

	// Expose the startManager callback so the setup API can trigger it.
	srv.SetStartManager(startManager)

	// Start agent manager if a provider is already configured.
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
		logger.Info("hive starting", "addr", listenAddr, "cluster", cs.listener.Addr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	pruneCancel()
	logger.Info("shutting down...")

	// Drain HTTP connections first so in-flight agent calls complete,
	// then shut down the agent manager and gRPC server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	err = httpServer.Shutdown(shutdownCtx)
	discoveryCancel()
	if relayLis != nil {
		relayLis.Close()
	}
	cs.grpcServer.GracefulStop()
	cs.fileSync.Stop()
	if mgr != nil {
		mgr.Shutdown()
	}
	if saveErr := cp.Save(); saveErr != nil {
		logger.Error("failed to save control plane config", "error", saveErr)
	}
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}


