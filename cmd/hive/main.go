package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/grpc"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/api"
	"github.com/nchapman/hivebot/internal/controlplane"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"github.com/nchapman/hivebot/internal/workspace"
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
	workspaceDir := envOr("HIVE_WORKSPACE_DIR", ".")
	providerType := envOr("HIVE_PROVIDER", "anthropic")
	apiKey := os.Getenv("HIVE_API_KEY")
	modelOverride := os.Getenv("HIVE_MODEL")

	absWorkspaceDir, _ := filepath.Abs(workspaceDir)
	cpPath := filepath.Join(absWorkspaceDir, "config.yaml")

	// Initialize workspace directory structure and seed defaults
	if err := workspace.Init(workspaceDir, logger); err != nil {
		return fmt.Errorf("initializing workspace: %w", err)
	}

	// Load control plane config (secrets, tool policies).
	cp, err := controlplane.Load(cpPath, logger)
	if err != nil {
		return fmt.Errorf("loading control plane: %w", err)
	}

	webFS, err := web.DistFS()
	if err != nil {
		return fmt.Errorf("loading web UI: %w", err)
	}

	// Set up signal handling early so the manager gets a cancellable context
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := api.NewServer(logger, webFS)
	srv.SetControlPlane(cp)

	// Start agent manager and leader agent if API key is set
	var mgr *agent.Manager
	var grpcSrv *grpc.Server
	if apiKey != "" {
		// Start host gRPC server for agent processes to connect to.
		hostSocket := filepath.Join(os.TempDir(), fmt.Sprintf("hive-host-%d.sock", os.Getpid()))
		os.Remove(hostSocket)
		hostLis, err := net.Listen("unix", hostSocket)
		if err != nil {
			return fmt.Errorf("listening on host socket: %w", err)
		}
		defer os.Remove(hostSocket)

		mgr = agent.NewManager(ctx, workspaceDir, agent.Options{
			Provider:   agent.ProviderType(providerType),
			APIKey:     apiKey,
			Model:      modelOverride,
			WorkingDir: absWorkspaceDir,
		}, cp, logger, hostSocket, nil)

		// Start gRPC server — Manager satisfies ipc.HostManager.
		grpcSrv = grpc.NewServer()
		grpcipc.NewHostServer(mgr).Register(grpcSrv)
		go func() {
			if err := grpcSrv.Serve(hostLis); err != nil {
				logger.Error("host gRPC server error", "error", err)
			}
		}()

		// Restore any persistent agents from previous run
		if err := mgr.RestoreSessions(ctx); err != nil {
			logger.Warn("failed to restore some agent sessions", "error", err)
		}

		// Start coordinator if not already restored from a previous run
		leaderID, alreadyRunning := mgr.AgentByName("coordinator")
		if !alreadyRunning {
			var err error
			leaderID, err = mgr.StartAgent(ctx, "coordinator", "")
			if err != nil {
				if os.IsNotExist(err) {
					logger.Info("no coordinator agent defined, skipping")
				} else {
					return fmt.Errorf("starting leader agent: %w", err)
				}
			}
		}
		if leaderID != "" {
			logger.Info("leader agent ready",
				"id", leaderID,
				"provider", providerType,
			)
			srv.SetManager(mgr, leaderID)
		}
	} else {
		logger.Info("no HIVE_API_KEY set — running without LLM (dashboard only)")
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
	if grpcSrv != nil {
		grpcSrv.GracefulStop() // stop accepting agent→host calls first
	}
	if mgr != nil {
		mgr.Shutdown() // then gracefully terminate all agents
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
