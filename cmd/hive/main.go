package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
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
	"github.com/nchapman/hivebot/internal/controlplane"
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

	swarmCode := os.Getenv("HIVE_SWARM_CODE")
	if swarmCode == "" {
		swarmCode = generateRandomCode()
		logger.Warn("HIVE_SWARM_CODE not set — generated ephemeral code",
			"code", swarmCode)
	}
	listenAddr := envOr("HIVE_ADDR", ":8080")
	rootDir := envOr("HIVE_ROOT", ".")

	absRootDir, _ := filepath.Abs(rootDir)
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

		// Watch agent definitions for config changes and push to running agents.
		mgr.WatchAgentDefinitions(fsWatcher)

		// Restore any persistent agents from previous run
		if err := mgr.RestoreSessions(ctx); err != nil {
			logger.Warn("failed to restore some agent sessions", "error", err)
		}

		// Start coordinator if not already restored from a previous run.
		// If a stopped coordinator exists, restart it rather than creating a duplicate.
		leaderID, alreadyRunning := mgr.SessionByAgentName("coordinator")
		if alreadyRunning {
			// Already running from RestoreSessions — nothing to do.
		} else if leaderID != "" {
			// Stopped coordinator found — restart it.
			if err := mgr.StartSession(ctx, leaderID); err != nil {
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
			leaderID, err = mgr.CreateSession(ctx, "coordinator", "", "coordinator")
			if err != nil {
				if os.IsNotExist(err) {
					logger.Info("no coordinator agent defined, skipping")
				} else {
					return fmt.Errorf("starting leader agent: %w", err)
				}
			}
		}
		if leaderID != "" {
			providerType, _, _ := cp.ProviderInfo()
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
		logger.Info("hive starting", "addr", listenAddr, "swarm", swarmCode)
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

func generateRandomCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
