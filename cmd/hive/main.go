package main

import (
	"context"
	"crypto/tls"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/api"
	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/controlplane"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"github.com/nchapman/hivebot/internal/platform"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
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

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	listenAddr := envOr("HIVE_ADDR", ":8080")
	rootDir := envOr("HIVE_ROOT", ".")

	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolving root dir: %w", err)
	}
	cpPath := filepath.Join(absRootDir, "config.yaml")

	// Initialize platform directory structure and seed defaults
	if err := platform.Init(rootDir, logger); err != nil {
		return fmt.Errorf("initializing platform: %w", err)
	}

	// Open the unified platform database.
	pdb, err := platformdb.Open(filepath.Join(absRootDir, "db", "hive.db"))
	if err != nil {
		return fmt.Errorf("opening platform database: %w", err)
	}
	defer pdb.Close()

	// Load control plane config (secrets, tool policies, providers).
	cp, err := controlplane.Load(cpPath, logger)
	if err != nil {
		return fmt.Errorf("loading control plane: %w", err)
	}

	// Check cluster mode: worker nodes take a completely different path.
	if cp.ClusterMode() == "worker" {
		return runWorkerNode(absRootDir, cp, logger)
	}

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

	srv := api.NewServer(logger, webFS)
	srv.SetRootDir(absRootDir)
	srv.SetControlPlane(cp)
	srv.SetDB(pdb)
	srv.SetWatcher(fsWatcher)

	// Load or create node identity for mTLS and tracker announcements.
	identity, err := cluster.LoadOrCreateIdentity(absRootDir)
	if err != nil {
		return fmt.Errorf("loading node identity: %w", err)
	}
	logger.Info("node identity loaded", "node_id", identity.NodeID[:16]+"...")

	tlsCert, err := cluster.TLSCertFromIdentity(identity)
	if err != nil {
		return fmt.Errorf("generating TLS certificate: %w", err)
	}
	logger.Info("cluster mTLS enabled", "fingerprint", cluster.TLSFingerprint(tlsCert)[:16]+"...")

	// Start cluster gRPC server for worker node connections (mTLS).
	clusterAddr := envOr("HIVE_CLUSTER_ADDR", ":8081")
	registry := cluster.NewNodeRegistry()
	registry.RegisterHome(envOr("HOSTNAME", "leader"))

	leaderStream := cluster.NewLeaderStream(registry, func(token string) string {
		return cp.ValidateJoinToken(token)
	}, logger)

	serverTLS := cluster.ServerTLSConfig(tlsCert)
	clusterGRPC := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	leaderStream.Register(clusterGRPC)

	clusterLis, err := net.Listen("tcp", clusterAddr)
	if err != nil {
		return fmt.Errorf("listening on cluster addr %s: %w", clusterAddr, err)
	}

	clusterSvc := cluster.NewLeaderService(leaderStream, registry, logger)

	// Set up file sync: leader watches workspace/agents/skills and pushes to nodes.
	leaderSync := cluster.NewFileSyncService(cluster.FileSyncConfig{
		RootDir:  absRootDir,
		SyncDirs: []string{"agents", "skills", "workspace"},
		NodeID:   "leader",
		SendFn: func(update *pb.FileUpdate) error {
			clusterSvc.BroadcastFileUpdate(update)
			return nil
		},
		Logger: logger,
	})
	clusterSvc.SetFileSync(leaderSync)
	go leaderSync.WatchAndSync()

	go func() {
		logger.Info("cluster gRPC server starting", "addr", clusterAddr)
		if err := clusterGRPC.Serve(clusterLis); err != nil {
			logger.Error("cluster gRPC error", "error", err)
		}
	}()

	// Start tracker discovery announcements if configured.
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)
	defer discoveryCancel()
	if trackerURL := cp.ClusterTrackerURL(); trackerURL != "" {
		swarmCode := cp.ClusterSwarmCode()
		if swarmCode == "" {
			return fmt.Errorf("cluster.swarm_code (or HIVE_SWARM_CODE) is required when tracker_url is set")
		}

		// Parse gRPC port from cluster address.
		_, portStr, _ := net.SplitHostPort(clusterAddr)
		grpcPort, _ := strconv.Atoi(portStr)

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

		// After first announce, check if we're reachable. If not, register
		// with the relay so workers can reach us through it.
		go func() {
			// Wait for the first announce to complete so we have yourIP and relayURL.
			time.Sleep(3 * time.Second)
			if dc.YourIP() == "" {
				return
			}

			publicAddr := fmt.Sprintf("%s:%d", dc.YourIP(), grpcPort)
			if cluster.SelfTestReachability(publicAddr) {
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

			// Relayed connections feed into the same gRPC server.
			rc.Run(discoveryCtx, func(conn net.Conn) {
				// Wrap the relayed connection with TLS and feed it to the gRPC server.
				// The gRPC server is already configured with ServerTLSConfig, but relayed
				// connections arrive as raw TCP. We need to manually wrap them in TLS.
				tlsConn := tls.Server(conn, serverTLS)
				if err := tlsConn.Handshake(); err != nil {
					logger.Warn("relay: mTLS handshake failed", "error", err)
					conn.Close()
					return
				}
				// Serve this single connection on the gRPC server.
				// grpc.Server doesn't have a ServeConn method, so we create a
				// single-connection listener.
				lis := newSingleConnListener(tlsConn)
				clusterGRPC.Serve(lis)
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

		// Start coordinator if not already restored from a previous run.
		// If a stopped coordinator exists, restart it rather than creating a duplicate.
		leaderID, alreadyRunning := mgr.InstanceByAgentName("coordinator")
		if alreadyRunning {
			// Already running from RestoreInstances — nothing to do.
		} else if leaderID != "" {
			// Stopped coordinator found — restart it.
			if err := mgr.StartInstance(ctx, leaderID); err != nil {
				if os.IsNotExist(err) {
					logger.Info("coordinator agent definition missing, skipping restart")
					leaderID = ""
				} else {
					return fmt.Errorf("restarting coordinator: %w", err)
				}
			}
		} else {
			// No coordinator at all — create one.
			var err error
			leaderID, err = mgr.CreateInstance(ctx, "coordinator", "", "coordinator", "")
			if err != nil {
				if os.IsNotExist(err) {
					logger.Info("no coordinator agent defined, skipping")
				} else {
					return fmt.Errorf("starting leader agent: %w", err)
				}
			}
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
		logger.Info("hive starting", "addr", listenAddr, "cluster", clusterAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	// Drain HTTP connections first so in-flight agent calls complete,
	// then shut down the agent manager and gRPC server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	err = httpServer.Shutdown(shutdownCtx)
	discoveryCancel()
	clusterGRPC.GracefulStop()
	leaderSync.Stop()
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

// singleConnListener wraps a single net.Conn as a net.Listener.
// Accept returns the connection once, then blocks until Close is called.
type singleConnListener struct {
	conn net.Conn
	ch   chan net.Conn
	done chan struct{}
}

func newSingleConnListener(conn net.Conn) net.Listener {
	l := &singleConnListener{
		conn: conn,
		ch:   make(chan net.Conn, 1),
		done: make(chan struct{}),
	}
	l.ch <- conn
	return l
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

